package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"hal9001/internal/spec"
)

// Credential variables of the ssh backend, read from its env file and never passed to commands.
const (
	envSSHPassword   = "SSH_PASSWORD"
	envSSHPrivateKey = "SSH_PRIVATE_KEY"
	envSSHPassphrase = "SSH_KEY_PASSPHRASE" //nolint:gosec // variable name, not a credential
)

// sshBackend runs commands on a remote host over SSH. The connection is shared by all sessions and
// re-established when it breaks; Close drops it.
type sshBackend struct {
	cfg     spec.Sandbox
	creds   map[string]string // full env, including credentials
	env     map[string]string // env for commands
	timeout time.Duration

	mu     sync.Mutex
	client *ssh.Client
}

func newSSHBackend(cfg spec.Sandbox, full, cmdEnv map[string]string) *sshBackend {
	return &sshBackend{cfg: cfg, creds: full, env: cmdEnv, timeout: cfg.Timeout.Std()}
}

func (b *sshBackend) authMethods() ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod
	key := []byte(b.creds[envSSHPrivateKey])
	if len(key) == 0 && b.cfg.KeyFile != "" {
		var err error
		if key, err = os.ReadFile(b.cfg.KeyFile); err != nil {
			return nil, fmt.Errorf("read key_file: %w", err)
		}
	}
	if len(key) > 0 {
		var signer ssh.Signer
		var err error
		if pass := b.creds[envSSHPassphrase]; pass != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase(key, []byte(pass))
		} else {
			signer, err = ssh.ParsePrivateKey(key)
		}
		if err != nil {
			return nil, fmt.Errorf("parse private key: %w", err)
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}
	if pw := b.creds[envSSHPassword]; pw != "" {
		methods = append(methods, ssh.Password(pw))
	}
	if len(methods) == 0 {
		return nil, fmt.Errorf("no ssh credentials: set key_file, or %s or %s in the sandbox environment", envSSHPrivateKey, envSSHPassword)
	}
	return methods, nil
}

func (b *sshBackend) dial(ctx context.Context) (*ssh.Client, error) {
	methods, err := b.authMethods()
	if err != nil {
		return nil, err
	}
	pinned := b.cfg.HostKey
	conf := &ssh.ClientConfig{
		User: b.cfg.User, Auth: methods, Timeout: 20 * time.Second,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			if fp := ssh.FingerprintSHA256(key); fp != pinned {
				return &HostKeyError{Fingerprint: fp, Pinned: pinned}
			}
			return nil
		},
	}
	addr := net.JoinHostPort(b.cfg.Host, strconv.Itoa(orInt(b.cfg.Port, 22)))
	var d net.Dialer
	nc, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = nc.Close() })
	defer stop()
	cc, chans, reqs, err := ssh.NewClientConn(nc, addr, conf)
	if err != nil {
		_ = nc.Close()
		var hk *HostKeyError
		if errors.As(err, &hk) {
			return nil, hk
		}
		return nil, err
	}
	return ssh.NewClient(cc, chans, reqs), nil
}

func (b *sshBackend) conn(ctx context.Context) (*ssh.Client, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.client != nil {
		return b.client, nil
	}
	c, err := b.dial(ctx)
	if err != nil {
		return nil, err
	}
	b.client = c
	return c, nil
}

func (b *sshBackend) drop(c *ssh.Client) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.client == c {
		b.client = nil
	}
	_ = c.Close()
}

// Close drops the shared connection.
func (b *sshBackend) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.client == nil {
		return nil
	}
	err := b.client.Close()
	b.client = nil
	return err
}

func (b *sshBackend) run(ctx context.Context, script string, stdin []byte, timeout time.Duration, limit int) (rawResult, error) {
	for attempt := 0; ; attempt++ {
		c, err := b.conn(ctx)
		if err != nil {
			return rawResult{}, err
		}
		sess, err := c.NewSession()
		if err != nil {
			b.drop(c)
			if attempt == 0 && ctx.Err() == nil {
				continue
			}
			return rawResult{}, err
		}
		return b.runSession(ctx, sess, script, stdin, timeout, limit)
	}
}

func (b *sshBackend) runSession(ctx context.Context, sess *ssh.Session, script string, stdin []byte, timeout time.Duration, limit int) (rawResult, error) {
	defer func() { _ = sess.Close() }()
	var out, errb limitBuffer
	out.limit, errb.limit = limit, limit
	sess.Stdout, sess.Stderr = &out, &errb
	if stdin != nil {
		sess.Stdin = bytes.NewReader(stdin)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := sess.Start("/bin/sh -c " + shQuote(script)); err != nil {
		return rawResult{}, err
	}
	done := make(chan error, 1)
	go func() { done <- sess.Wait() }()
	res := rawResult{}
	var err error
	select {
	case err = <-done:
	case <-ctx.Done():
		_ = sess.Signal(ssh.SIGKILL)
		_ = sess.Close()
		res.TimedOut, res.ExitCode = true, -1
		if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return res, ctx.Err()
		}
	}
	res.Stdout, res.Stderr, res.Truncated = out.Bytes(), errb.Bytes(), out.wasTruncated() || errb.wasTruncated()
	var ee *ssh.ExitError
	switch {
	case res.TimedOut, err == nil:
	case errors.As(err, &ee):
		res.ExitCode = ee.ExitStatus()
	default:
		return res, err
	}
	return res, nil
}

func (b *sshBackend) check(ctx context.Context) CheckResult {
	res, err := b.run(ctx, "mkdir -p "+shQuote(b.cfg.Root)+" && cd "+shQuote(b.cfg.Root)+" && pwd -P", nil, 30*time.Second, 4096)
	var hk *HostKeyError
	switch {
	case errors.As(err, &hk) && hk.Pinned == "":
		return CheckResult{Fingerprint: hk.Fingerprint, Message: "The host key is not pinned yet. Compare this fingerprint with the server's and confirm it: " + hk.Fingerprint}
	case errors.As(err, &hk):
		return CheckResult{Fingerprint: hk.Fingerprint, Message: hk.Error()}
	case err != nil:
		return CheckResult{Message: err.Error()}
	case res.ExitCode != 0:
		return CheckResult{Message: "cannot create the root directory: " + firstLine(string(res.Stderr))}
	}
	return CheckResult{OK: true, Message: fmt.Sprintf("connected to %s@%s, root %s", b.cfg.User, b.cfg.Host, strings.TrimSpace(string(res.Stdout)))}
}

func (b *sshBackend) Prepare(ctx context.Context, rs RunSpec) (Session, error) {
	if !spec.ValidID(rs.RunID) {
		return nil, fmt.Errorf("invalid run id %q", rs.RunID)
	}
	perRun := orDefault(rs.Lifecycle, b.cfg.Lifecycle) == spec.LifecyclePerRun
	dir := b.cfg.Root
	if perRun {
		dir = path.Join(b.cfg.Root, "runs", rs.RunID)
	}
	res, err := b.run(ctx, "mkdir -p "+shQuote(dir)+" && cd "+shQuote(dir)+" && pwd -P", nil, time.Minute, 4096)
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("create workdir: %s", firstLine(string(res.Stderr)))
	}
	workdir := strings.TrimSpace(string(res.Stdout))
	s := &remoteSession{
		workdir: workdir, run: b.run, timeout: b.timeout, env: mergeEnv(b.env, rs.Env), inlineEnv: true,
		closeFn: func(ctx context.Context, success bool) error {
			if perRun && success {
				_, err := b.run(ctx, "rm -rf -- "+shQuote(workdir), nil, time.Minute, 4096)
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

// Sweep removes run directories of failed runs older than maxAge.
func (b *sshBackend) Sweep(ctx context.Context, maxAge time.Duration) error {
	mins := max(int(maxAge/time.Minute), 1)
	script := fmt.Sprintf("[ -d %s ] && find %s -mindepth 1 -maxdepth 1 -mmin +%d -exec rm -rf -- {} +; exit 0",
		shQuote(path.Join(b.cfg.Root, "runs")), shQuote(path.Join(b.cfg.Root, "runs")), mins)
	_, err := b.run(ctx, script, nil, time.Minute, 4096)
	return err
}

func orInt(v, def int) int {
	if v == 0 {
		return def
	}
	return v
}
