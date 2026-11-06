package sandbox

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"hal9001/internal/spec"
)

// sshTestServer is an SSH server that runs exec requests with the local /bin/sh. It accepts the password "pw".
type sshTestServer struct {
	host        string
	port        int
	fingerprint string
}

func startSSHServer(t *testing.T) *sshTestServer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	conf := &ssh.ServerConfig{PasswordCallback: func(_ ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
		if string(pw) == "pw" {
			return nil, nil
		}
		return nil, errors.New("denied")
	}}
	conf.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			go serveSSH(nc, conf)
		}
	}()
	addr := ln.Addr().(*net.TCPAddr)
	return &sshTestServer{host: "127.0.0.1", port: addr.Port, fingerprint: ssh.FingerprintSHA256(signer.PublicKey())}
}

func serveSSH(nc net.Conn, conf *ssh.ServerConfig) {
	sc, chans, reqs, err := ssh.NewServerConn(nc, conf)
	if err != nil {
		return
	}
	defer func() { _ = sc.Close() }()
	go ssh.DiscardRequests(reqs)
	for nch := range chans {
		if nch.ChannelType() != "session" {
			_ = nch.Reject(ssh.UnknownChannelType, "no")
			continue
		}
		ch, creqs, err := nch.Accept()
		if err != nil {
			return
		}
		go func() {
			for r := range creqs {
				if r.Type != "exec" {
					_ = r.Reply(false, nil)
					continue
				}
				_ = r.Reply(true, nil)
				cmdline := string(r.Payload[4:])
				c := exec.Command("/bin/sh", "-c", cmdline)
				c.Stdin = ch
				c.Stdout = ch
				c.Stderr = ch.Stderr()
				code := 0
				if err := c.Run(); err != nil {
					var ee *exec.ExitError
					if errors.As(err, &ee) {
						code = ee.ExitCode()
					} else {
						code = 127
					}
				}
				_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Code uint32 }{uint32(code)}))
				_ = ch.Close()
			}
		}()
	}
}

func newSSH(t *testing.T, srv *sshTestServer, hostKey string, env Env) (Backend, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "root")
	cfg := spec.DefaultSandbox(spec.SandboxSSH)
	cfg.Enabled, cfg.Host, cfg.Port, cfg.User, cfg.Root, cfg.HostKey = true, srv.host, srv.port, "u", root, hostKey
	b, err := New(spec.SandboxSSH, cfg, env, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.(io.Closer).Close() })
	return b, root
}

func TestSSHHostKeyPinning(t *testing.T) {
	srv := startSSHServer(t)
	env := Env{Vars: map[string]string{"SSH_PASSWORD": "pw"}, Secrets: []string{"pw"}}
	cfg := spec.DefaultSandbox(spec.SandboxSSH)
	cfg.Enabled, cfg.Host, cfg.Port, cfg.User, cfg.Root = true, srv.host, srv.port, "u", filepath.Join(t.TempDir(), "r")

	res := Check(context.Background(), spec.SandboxSSH, cfg, env, "")
	if res.OK || res.Fingerprint != srv.fingerprint {
		t.Fatalf("unpinned check = %+v, want failure carrying the fingerprint %s", res, srv.fingerprint)
	}
	cfg.HostKey = "SHA256:" + strings.Repeat("A", 43)
	res = Check(context.Background(), spec.SandboxSSH, cfg, env, "")
	if res.OK || res.Fingerprint != srv.fingerprint || !strings.Contains(res.Message, "mismatch") {
		t.Fatalf("wrong pin check = %+v", res)
	}
	cfg.HostKey = srv.fingerprint
	if res = Check(context.Background(), spec.SandboxSSH, cfg, env, ""); !res.OK {
		t.Fatalf("pinned check = %+v", res)
	}
	cfg.HostKey = srv.fingerprint
	env.Vars["SSH_PASSWORD"] = "wrong"
	if res = Check(context.Background(), spec.SandboxSSH, cfg, env, ""); res.OK {
		t.Fatalf("wrong password accepted: %+v", res)
	}

	b, _ := newSSH(t, srv, "", env)
	var hk *HostKeyError
	if _, err := b.Prepare(context.Background(), RunSpec{RunID: "r1"}); !errors.As(err, &hk) {
		t.Fatalf("Prepare on an unpinned host = %v, want *HostKeyError", err)
	}
}

func TestSSHSessionOverRemoteScripts(t *testing.T) {
	ctx := context.Background()
	srv := startSSHServer(t)
	env := Env{Vars: map[string]string{"SSH_PASSWORD": "pw", "TOKEN_X": "tok-abcdef", "KEEP": "visible"}, Secrets: []string{"tok-abcdef"}}
	b, root := newSSH(t, srv, srv.fingerprint, env)
	s, err := b.Prepare(ctx, RunSpec{RunID: "r1", Input: []byte("[1]")})
	if err != nil {
		t.Fatal(err)
	}
	wd, _ := filepath.EvalSymlinks(filepath.Join(root, "runs", "r1"))
	if s.Workdir() != wd {
		t.Fatalf("workdir = %q, want %q", s.Workdir(), wd)
	}
	res, err := s.Exec(ctx, `echo "$KEEP $TOKEN_X [$SSH_PASSWORD]"; cat; exit 2`, []byte("stdin!"), 0)
	if err != nil || res.ExitCode != 2 || !strings.Contains(res.Stdout, "visible "+Redacted+" []") || !strings.HasSuffix(res.Stdout, "stdin!") {
		t.Fatalf("exec = %+v, %v", res, err)
	}
	if got, err := s.ReadFile(ctx, "input.json"); err != nil || string(got) != "[1]" {
		t.Fatalf("input.json = %q, %v", got, err)
	}
	payload := []byte("line one\n\x00binary-ish\n'quotes' \"dq\" $HOME `x`\n")
	if err := s.WriteFile(ctx, "a/b/c.bin", payload); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ReadFile(ctx, "a/b/c.bin"); err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("read back = %q, %v", got, err)
	}
	if err := s.WriteFile(ctx, "a/t.txt", []byte("one\nline needle\n")); err != nil {
		t.Fatal(err)
	}
	ents, err := s.List(ctx, ".")
	if err != nil || len(ents) != 2 || ents[0].Name != "a" || !ents[0].IsDir || ents[1].Name != "input.json" || ents[1].Size != 3 {
		t.Fatalf("list = %+v, %v", ents, err)
	}
	hits, err := s.Search(ctx, ".", "needle")
	if err != nil || len(hits) != 1 || hits[0].Path != "a/t.txt" || hits[0].Line != 2 {
		t.Fatalf("search = %+v, %v", hits, err)
	}
	if _, err := s.ReadFile(ctx, "nope.txt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing = %v", err)
	}

	outside := t.TempDir()
	_ = os.WriteFile(filepath.Join(outside, "secret"), []byte("top"), 0o600)
	_ = os.Symlink(outside, filepath.Join(wd, "dirlink"))
	_ = os.Symlink(filepath.Join(outside, "secret"), filepath.Join(wd, "filelink"))
	for _, p := range []string{"../x", "/etc/passwd", "dirlink/secret", "filelink"} {
		if _, err := s.ReadFile(ctx, p); !errors.Is(err, ErrEscape) {
			t.Errorf("ReadFile(%q) = %v, want ErrEscape", p, err)
		}
	}
	for _, p := range []string{"../x", "dirlink/new", "dirlink/sub/new", "filelink"} {
		if err := s.WriteFile(ctx, p, []byte("x")); !errors.Is(err, ErrEscape) {
			t.Errorf("WriteFile(%q) = %v, want ErrEscape", p, err)
		}
	}
	if _, err := os.Stat(filepath.Join(outside, "sub")); err == nil {
		t.Error("mkdir -p followed a symlink out of the workdir")
	}
	if got, _ := os.ReadFile(filepath.Join(outside, "secret")); string(got) != "top" {
		t.Error("write through a file symlink changed the target")
	}
	if _, err := s.List(ctx, "dirlink"); !errors.Is(err, ErrEscape) {
		t.Errorf("List(dirlink) = %v", err)
	}

	if err := s.Close(ctx, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(wd); !os.IsNotExist(err) {
		t.Error("per_run workdir kept after a successful run")
	}
}
