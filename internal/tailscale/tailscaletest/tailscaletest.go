// Package tailscaletest provides a stub `tailscale` executable for tests and screenshots. It keeps its state in
// files, records every invocation and never touches a real Tailscale.
package tailscaletest

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

//go:embed stub.sh
var script string

// DefaultDNS and DefaultOwner are the fake machine name and owner login of a fresh stub.
const (
	DefaultDNS   = "hal-mac.example-tailnet.ts.net"
	DefaultOwner = "me@example.com"
)

// Script returns the stub script with stateDir baked in as the default value of $TS_STUB_DIR.
func Script(stateDir string) string { return strings.ReplaceAll(script, "@STATE_DIR@", stateDir) }

// Stub is one stub CLI with its state directory.
type Stub struct {
	t *testing.T
	// BinDir holds the executable `tailscale`; put it on a restricted PATH.
	BinDir string
	// Dir is the state directory.
	Dir string
}

// New writes the stub for a signed-in machine named DefaultDNS with HTTPS certificates enabled and nothing served.
func New(t *testing.T) *Stub {
	t.Helper()
	root := t.TempDir()
	s := &Stub{t: t, BinDir: filepath.Join(root, "bin"), Dir: filepath.Join(root, "state")}
	for _, d := range []string{s.BinDir, s.Dir} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(s.BinDir, "tailscale"), []byte(Script(s.Dir)), 0o700); err != nil { //nolint:gosec // the stub must be executable
		t.Fatal(err)
	}
	s.SetStatus(StatusOpts{})
	return s
}

// StatusOpts describes `tailscale status --json`. The zero value is a signed-in machine named DefaultDNS with MagicDNS
// and HTTPS certificates on.
type StatusOpts struct {
	DNSName      string // default DefaultDNS
	Owner        string // default DefaultOwner
	BackendState string // default Running
	NoHTTPS      bool   // CertDomains empty
	NoMagicDNS   bool
}

// StatusJSON renders o.
func StatusJSON(o StatusOpts) (status, dns string) {
	if o.DNSName == "" {
		o.DNSName = DefaultDNS
	}
	if o.Owner == "" {
		o.Owner = DefaultOwner
	}
	if o.BackendState == "" {
		o.BackendState = "Running"
	}
	certs := fmt.Sprintf("[%q]", o.DNSName)
	if o.NoHTTPS {
		certs = "[]"
	}
	suffix := o.DNSName[strings.Index(o.DNSName, ".")+1:]
	return fmt.Sprintf(`{"Version":"1.80.2","BackendState":%q,"Self":{"HostName":"hal-mac","DNSName":"%s.","UserID":42},`+
		`"CurrentTailnet":{"Name":"example-tailnet.org","MagicDNSSuffix":%q,"MagicDNSEnabled":%t},"CertDomains":%s,`+
		`"User":{"42":{"LoginName":%q}}}`, o.BackendState, o.DNSName, suffix, !o.NoMagicDNS, certs, o.Owner), o.DNSName
}

// SetStatus replaces the status the stub reports.
func (s *Stub) SetStatus(o StatusOpts) {
	s.t.Helper()
	status, dns := StatusJSON(o)
	s.Write("status.json", status)
	s.Write("dnsname", dns)
}

// Write writes a state file.
func (s *Stub) Write(name, content string) {
	s.t.Helper()
	if err := os.WriteFile(filepath.Join(s.Dir, name), []byte(content), 0o600); err != nil {
		s.t.Fatal(err)
	}
}

// Remove deletes a state file; a missing file is fine.
func (s *Stub) Remove(name string) {
	s.t.Helper()
	if err := os.Remove(filepath.Join(s.Dir, name)); err != nil && !os.IsNotExist(err) {
		s.t.Fatal(err)
	}
}

// Serve sets the mapping on an HTTPS port, as if `tailscale serve` had been run before.
func (s *Stub) Serve(port int, target string) { s.Write(fmt.Sprintf("serve-%d", port), target+"\n") }

// Served returns the target mapped on an HTTPS port, or "".
func (s *Stub) Served(port int) string {
	b, err := os.ReadFile(filepath.Join(s.Dir, fmt.Sprintf("serve-%d", port))) //nolint:gosec // test state file
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// Calls returns every invocation so far as the space-joined arguments.
func (s *Stub) Calls() []string {
	b, err := os.ReadFile(filepath.Join(s.Dir, "calls.log")) //nolint:gosec // test state file
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
}

// Mutations returns the invocations that change state: every `serve` except `serve status`.
func (s *Stub) Mutations() []string {
	var out []string
	for _, c := range s.Calls() {
		if strings.HasPrefix(c, "serve ") && !strings.HasPrefix(c, "serve status") {
			out = append(out, c)
		}
	}
	return out
}
