package sandbox

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"hal9001/internal/spec"
)

// dockerBackend talks to the Docker Engine API on a unix socket (or tcp://host:port).
type dockerBackend struct {
	cfg     spec.Sandbox
	cli     *dockerClient
	env     map[string]string
	timeout time.Duration

	mu sync.Mutex // serializes creation of persistent containers
}

const dockerWorkRoot = "/tmp/hal"

// dockerSocket resolves the Engine endpoint: the configured socket, DOCKER_HOST, then the usual socket
// locations (Docker Desktop, Colima, rootless).
func dockerSocket(cfg spec.Sandbox, env map[string]string) string {
	if cfg.Socket != "" {
		return cfg.Socket
	}
	if h := env["DOCKER_HOST"]; h != "" {
		return h
	}
	if h := os.Getenv("DOCKER_HOST"); h != "" {
		return h
	}
	home, _ := os.UserHomeDir()
	for _, p := range []string{
		"/var/run/docker.sock",
		filepath.Join(home, ".colima/default/docker.sock"),
		filepath.Join(home, ".docker/run/docker.sock"),
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "/var/run/docker.sock"
}

func newDockerBackend(cfg spec.Sandbox, env map[string]string) *dockerBackend {
	return &dockerBackend{cfg: cfg, cli: newDockerClient(dockerSocket(cfg, env)), env: env, timeout: cfg.Timeout.Std()}
}

func (b *dockerBackend) check(ctx context.Context) CheckResult {
	var ver struct{ Version, ApiVersion string }
	if err := b.cli.json(ctx, http.MethodGet, "/version", nil, &ver); err != nil {
		return CheckResult{Message: "cannot reach the Docker Engine at " + b.cli.endpoint + ": " + err.Error()}
	}
	msg := fmt.Sprintf("Docker Engine %s (API %s) at %s", ver.Version, ver.ApiVersion, b.cli.endpoint)
	return CheckResult{OK: true, Message: msg}
}

func (b *dockerBackend) image(rs RunSpec) (string, error) {
	img := rs.Image
	if img == "" && len(b.cfg.Images) > 0 {
		img = b.cfg.Images[0]
	}
	if img == "" {
		return "", errors.New("no docker image configured")
	}
	if !containsStr(b.cfg.Images, img) {
		return "", fmt.Errorf("image %q is not in the sandbox's image list", img)
	}
	return img, nil
}

func (b *dockerBackend) Prepare(ctx context.Context, rs RunSpec) (Session, error) {
	if !spec.ValidID(rs.RunID) {
		return nil, fmt.Errorf("invalid run id %q", rs.RunID)
	}
	img, err := b.image(rs)
	if err != nil {
		return nil, err
	}
	lifecycle := orDefault(rs.Lifecycle, b.cfg.Lifecycle)
	perRun := lifecycle == spec.LifecyclePerRun
	name := "hal-run-" + rs.RunID
	if !perRun {
		name = "hal-persistent-" + shortHash(img)
	}
	b.mu.Lock()
	err = b.ensureContainer(ctx, name, img, rs.RunID)
	b.mu.Unlock()
	if err != nil {
		return nil, err
	}
	env := envList(mergeEnv(b.env, rs.Env))
	run := func(ctx context.Context, script string, stdin []byte, timeout time.Duration, limit int) (rawResult, error) {
		return b.cli.exec(ctx, name, script, stdin, env, timeout, limit)
	}
	workdir := dockerWorkRoot + "/" + rs.RunID
	res, err := run(ctx, "mkdir -p "+shQuote(workdir)+" && cd "+shQuote(workdir)+" && pwd -P", nil, time.Minute, 4096)
	if err != nil || res.ExitCode != 0 {
		b.removeOnFailure(ctx, name, perRun)
		if err == nil {
			err = fmt.Errorf("create workdir: %s", strings.TrimSpace(string(res.Stderr)))
		}
		return nil, err
	}
	s := &remoteSession{
		workdir: strings.TrimSpace(string(res.Stdout)), run: run, timeout: b.timeout,
		closeFn: func(ctx context.Context, success bool) error {
			if perRun {
				if success {
					return b.cli.removeContainer(ctx, name)
				}
				return b.cli.stopContainer(ctx, name)
			}
			if success {
				_, err := run(ctx, "rm -rf -- "+shQuote(workdir), nil, time.Minute, 4096)
				return err
			}
			return nil
		},
	}
	if err := seed(ctx, s, rs); err != nil {
		_ = s.Close(ctx, false)
		return nil, err
	}
	return s, nil
}

func (b *dockerBackend) removeOnFailure(ctx context.Context, name string, perRun bool) {
	if perRun {
		_ = b.cli.removeContainer(ctx, name)
	}
}

// ensureContainer makes sure container name exists and is running, creating it from img if needed.
func (b *dockerBackend) ensureContainer(ctx context.Context, name, img, runID string) error {
	var info struct{ State struct{ Running bool } }
	err := b.cli.json(ctx, http.MethodGet, "/containers/"+name+"/json", nil, &info)
	switch {
	case err == nil && info.State.Running:
		return nil
	case err == nil:
		return b.cli.startContainer(ctx, name)
	case !isNotFound(err):
		return err
	}
	if err := b.cli.ensureImage(ctx, img); err != nil {
		return err
	}
	host := map[string]any{"NetworkMode": "default"}
	if !b.cfg.Network {
		host["NetworkMode"] = "none"
	}
	if b.cfg.Resources.MemoryMB > 0 {
		host["Memory"] = int64(b.cfg.Resources.MemoryMB) << 20
	}
	if b.cfg.Resources.CPUs > 0 {
		host["NanoCpus"] = int64(b.cfg.Resources.CPUs * 1e9)
	}
	body := map[string]any{
		"Image":      img,
		"Entrypoint": []string{"/bin/sh", "-c", "while :; do sleep 3600; done"},
		"Cmd":        []string{},
		"Labels":     map[string]string{"hal9001.sandbox": "1", "hal9001.run": runID},
		"HostConfig": host,
	}
	var created struct{ Id string }
	if err := b.cli.json(ctx, http.MethodPost, "/containers/create?name="+url.QueryEscape(name), body, &created); err != nil {
		return fmt.Errorf("create container: %w", err)
	}
	if err := b.cli.startContainer(ctx, name); err != nil {
		_ = b.cli.removeContainer(ctx, name)
		return err
	}
	return nil
}

// Sweep removes stopped per_run containers left by failed runs.
func (b *dockerBackend) Sweep(ctx context.Context, maxAge time.Duration) error {
	var list []struct {
		Id      string
		Names   []string
		State   string
		Created int64
	}
	filters := url.QueryEscape(`{"label":["hal9001.sandbox=1"],"status":["exited","created"]}`)
	if err := b.cli.json(ctx, http.MethodGet, "/containers/json?all=true&filters="+filters, nil, &list); err != nil {
		return err
	}
	for _, c := range list {
		if time.Since(time.Unix(c.Created, 0)) > maxAge {
			_ = b.cli.removeContainer(ctx, c.Id)
		}
	}
	return nil
}

// ---- Engine API client ----

type dockerClient struct {
	endpoint string
	network  string
	addr     string
	http     *http.Client
}

type dockerError struct {
	status int
	msg    string
}

func (e *dockerError) Error() string { return fmt.Sprintf("docker: %s (HTTP %d)", e.msg, e.status) }

func isNotFound(err error) bool {
	var de *dockerError
	return errors.As(err, &de) && de.status == http.StatusNotFound
}

func newDockerClient(endpoint string) *dockerClient {
	c := &dockerClient{endpoint: endpoint, network: "unix", addr: endpoint}
	switch {
	case strings.HasPrefix(endpoint, "unix://"):
		c.addr = strings.TrimPrefix(endpoint, "unix://")
	case strings.HasPrefix(endpoint, "tcp://"):
		c.network, c.addr = "tcp", strings.TrimPrefix(endpoint, "tcp://")
	}
	c.http = &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, c.network, c.addr)
		},
		DisableKeepAlives: true,
	}}
	return c
}

func (c *dockerClient) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://docker"+path, rd)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusNotModified {
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		var m struct{ Message string }
		msg := strings.TrimSpace(string(b))
		if json.Unmarshal(b, &m) == nil && m.Message != "" {
			msg = m.Message
		}
		return nil, &dockerError{resp.StatusCode, msg}
	}
	return resp, nil
}

func (c *dockerClient) json(ctx context.Context, method, path string, body, out any) error {
	resp, err := c.do(ctx, method, path, body)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *dockerClient) startContainer(ctx context.Context, id string) error {
	return c.json(ctx, http.MethodPost, "/containers/"+id+"/start", nil, nil)
}

func (c *dockerClient) stopContainer(ctx context.Context, id string) error {
	return c.json(ctx, http.MethodPost, "/containers/"+id+"/stop?t=1", nil, nil)
}

func (c *dockerClient) removeContainer(ctx context.Context, id string) error {
	err := c.json(ctx, http.MethodDelete, "/containers/"+id+"?force=true&v=true", nil, nil)
	if isNotFound(err) {
		return nil
	}
	return err
}

// ensureImage pulls img unless it is present.
func (c *dockerClient) ensureImage(ctx context.Context, img string) error {
	err := c.json(ctx, http.MethodGet, "/images/"+img+"/json", nil, nil)
	if err == nil {
		return nil
	}
	if !isNotFound(err) {
		return err
	}
	resp, err := c.do(ctx, http.MethodPost, "/images/create?fromImage="+url.QueryEscape(img), nil)
	if err != nil {
		return fmt.Errorf("pull %s: %w", img, err)
	}
	defer func() { _ = resp.Body.Close() }()
	dec := json.NewDecoder(resp.Body)
	for {
		var m struct {
			Error string `json:"error"`
		}
		if err := dec.Decode(&m); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if m.Error != "" {
			return fmt.Errorf("pull %s: %s", img, m.Error)
		}
	}
}

// exec runs script in container id and returns its demultiplexed output.
func (c *dockerClient) exec(ctx context.Context, id, script string, stdin []byte, env []string, timeout time.Duration, limit int) (rawResult, error) {
	var created struct{ Id string }
	err := c.json(ctx, http.MethodPost, "/containers/"+id+"/exec", map[string]any{
		"AttachStdin": stdin != nil, "AttachStdout": true, "AttachStderr": true,
		"Cmd": []string{"/bin/sh", "-c", script}, "Env": env,
	}, &created)
	if err != nil {
		return rawResult{}, fmt.Errorf("docker exec: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, br, err := c.hijack(ctx, "/exec/"+created.Id+"/start", map[string]any{"Detach": false, "Tty": false})
	if err != nil {
		return rawResult{}, err
	}
	defer func() { _ = conn.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if stdin != nil {
		go func() {
			_, _ = conn.Write(stdin)
			if cw, ok := conn.(interface{ CloseWrite() error }); ok {
				_ = cw.CloseWrite()
			}
		}()
	}
	var out, errb limitBuffer
	out.limit, errb.limit = limit, limit
	demuxErr := demux(br, &out, &errb)
	res := rawResult{Stdout: out.Bytes(), Stderr: errb.Bytes(), Truncated: out.wasTruncated() || errb.wasTruncated()}
	if ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			res.TimedOut, res.ExitCode = true, -1
			return res, nil
		}
		return res, ctx.Err()
	}
	if demuxErr != nil {
		return res, demuxErr
	}
	var info struct {
		ExitCode int
		Running  bool
	}
	ictx, icancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer icancel()
	if err := c.json(ictx, http.MethodGet, "/exec/"+created.Id+"/json", nil, &info); err != nil {
		return res, err
	}
	res.ExitCode = info.ExitCode
	return res, nil
}

// hijack POSTs body to path asking for a raw stream and returns the connection and its buffered reader.
func (c *dockerClient) hijack(ctx context.Context, path string, body any) (net.Conn, *bufio.Reader, error) {
	b, _ := json.Marshal(body)
	var d net.Dialer
	conn, err := d.DialContext(ctx, c.network, c.addr)
	if err != nil {
		return nil, nil, err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://docker"+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "tcp")
	if err := req.Write(conn); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req) //nolint:bodyclose // the body is the raw stream the caller reads from conn
	if err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols && resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		_ = conn.Close()
		return nil, nil, &dockerError{resp.StatusCode, strings.TrimSpace(string(msg))}
	}
	return conn, br, nil
}

// demux reads Docker's multiplexed stream (8-byte header: stream, 0, 0, 0, big-endian length).
func demux(r io.Reader, stdout, stderr io.Writer) error {
	var hdr [8]byte
	for {
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		n := int64(binary.BigEndian.Uint32(hdr[4:]))
		w := stdout
		if hdr[0] == 2 {
			w = stderr
		}
		if _, err := io.CopyN(w, r, n); err != nil {
			return err
		}
	}
}

func shortHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])[:12]
}

func containsStr(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
