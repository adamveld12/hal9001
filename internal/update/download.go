package update

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"
)

// Download limits. There is deliberately no overall timeout of a few minutes: a build is tens of megabytes and the
// link may be slow. A download that stops receiving bytes is what ends it, plus a generous cap against a trickle.
const (
	downloadConnectTimeout = 15 * time.Second // dial and TLS handshake
	downloadHeaderTimeout  = 30 * time.Second // request sent until the response headers arrive
	downloadMaxTime        = 30 * time.Minute
	downloadProgressEvery  = 250 * time.Millisecond
	downloadMaxRedirects   = 10
)

// downloadIdleTimeout is how long a download may receive no body bytes before it is given up as stalled (a variable
// so that a test can shorten it).
var downloadIdleTimeout = time.Minute

var (
	errDownloadStalled = errors.New("no data arrived for a minute")
	errDownloadTooLong = fmt.Errorf("it took longer than %s", downloadMaxTime)
)

// downloadHTTP is the client used when the caller passes none. Compression stays off: the body is a zip and the size
// and signature in the manifest are those of the bytes on the wire.
var downloadHTTP = &http.Client{Transport: &http.Transport{
	Proxy:                 http.ProxyFromEnvironment,
	DialContext:           (&net.Dialer{Timeout: downloadConnectTimeout, KeepAlive: 30 * time.Second}).DialContext,
	TLSHandshakeTimeout:   downloadConnectTimeout,
	ResponseHeaderTimeout: downloadHeaderTimeout,
	DisableCompression:    true,
}}

// Download fetches rawURL into dest through hc (nil: a client with connect and response-header timeouts). Only https
// is allowed, and http only to a loopback host (tests); redirects are followed under the same rule, since GitHub
// serves release assets from another host. The response must be exactly wantSize bytes: a larger one is refused
// without reading more than wantSize+1 bytes. The body goes to dest+".part", which is renamed to dest when complete
// and removed on any failure. progress, if set, is called with the bytes received so far, at most every 250 ms and
// once more at the end.
func Download(ctx context.Context, hc *http.Client, rawURL, dest string, wantSize int64, progress func(done int64)) (err error) {
	if wantSize <= 0 {
		return fmt.Errorf("the manifest gives no size (%d)", wantSize)
	}
	u, err := checkDownloadURL(rawURL)
	if err != nil {
		return err
	}
	if hc == nil {
		hc = downloadHTTP
	}
	client := *hc
	inner := hc.CheckRedirect
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if _, err := checkDownloadURL(req.URL.String()); err != nil {
			return err
		}
		if inner != nil {
			return inner(req, via)
		}
		if len(via) >= downloadMaxRedirects {
			return fmt.Errorf("stopped after %d redirects", downloadMaxRedirects)
		}
		return nil
	}

	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	tooLong := time.AfterFunc(downloadMaxTime, func() { cancel(errDownloadTooLong) })
	defer tooLong.Stop()

	part := dest + ".part"
	defer func() {
		if err != nil {
			_ = os.Remove(part)
		}
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept-Encoding", "identity") // also stops a custom transport from decompressing
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("could not reach %s: %w", u.Host, failureOf(ctx, err))
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("the server answered %s", resp.Status)
	case resp.ContentLength >= 0 && resp.ContentLength != wantSize:
		return fmt.Errorf("the server announces %d bytes, the manifest says %d", resp.ContentLength, wantSize)
	}

	f, err := os.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) //nolint:gosec // dest is chosen by the caller (the app's cache directory)
	if err != nil {
		return err
	}
	idle := time.AfterFunc(downloadIdleTimeout, func() { cancel(errDownloadStalled) })
	defer idle.Stop()
	w := &countingWriter{w: f, progress: progress}
	// One byte more than expected tells a too-long response from one of the right size without reading all of it.
	_, copyErr := io.Copy(w, &idleReader{r: io.LimitReader(resp.Body, wantSize+1), timer: idle})
	closeErr := f.Close()
	switch {
	case copyErr != nil:
		return fmt.Errorf("lost the connection after %d of %d bytes: %w", w.n, wantSize, failureOf(ctx, copyErr))
	case closeErr != nil:
		return closeErr
	case w.n > wantSize:
		return fmt.Errorf("the server sent more than the %d bytes the manifest says", wantSize)
	case w.n < wantSize:
		return fmt.Errorf("the server sent %d of the %d bytes the manifest says", w.n, wantSize)
	}
	if progress != nil {
		progress(w.n)
	}
	return os.Rename(part, dest)
}

// checkDownloadURL applies the scheme rule of Download to the first request and to every redirect.
func checkDownloadURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil, errors.New("the update URL is not usable")
	}
	if u.Scheme != "https" && (u.Scheme != "http" || !isLoopbackHost(u.Hostname())) {
		return nil, fmt.Errorf("refusing %s://%s: updates are fetched over https only", u.Scheme, u.Host)
	}
	return u, nil
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// failureOf names why a request or body read failed when the cause is one of ours (stall, time cap) or the caller
// cancelled, which the transport reports only as "context canceled".
func failureOf(ctx context.Context, err error) error {
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	// Do wraps its failure in a *url.Error that repeats the URL; after a redirect that is a signed one.
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// idleReader restarts the stall timer on every read that returns data.
type idleReader struct {
	r     io.Reader
	timer *time.Timer
}

func (r *idleReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if n > 0 {
		r.timer.Reset(downloadIdleTimeout)
	}
	return n, err
}

// countingWriter counts what it writes and reports it, rate limited.
type countingWriter struct {
	w        io.Writer
	n        int64
	progress func(int64)
	next     time.Time
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	if c.progress != nil {
		if now := time.Now(); now.After(c.next) {
			c.progress(c.n)
			c.next = now.Add(downloadProgressEvery)
		}
	}
	return n, err
}
