package service

import (
	"context"
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// plistDict decodes the top-level <dict> of a property list: strings, bools, []string for arrays, nested maps.
func plistDict(t *testing.T, data []byte) map[string]any {
	t.Helper()
	dec := xml.NewDecoder(strings.NewReader(string(data)))
	var value func(start xml.StartElement) any
	dict := func() map[string]any {
		m := map[string]any{}
		var key string
		for {
			tok, err := dec.Token()
			if err != nil {
				t.Fatalf("plist is not well-formed XML: %v", err)
			}
			switch el := tok.(type) {
			case xml.StartElement:
				if el.Name.Local == "key" {
					if err := dec.DecodeElement(&key, &el); err != nil {
						t.Fatal(err)
					}
				} else {
					m[key] = value(el)
				}
			case xml.EndElement:
				return m
			}
		}
	}
	value = func(start xml.StartElement) any {
		switch start.Name.Local {
		case "string":
			var s string
			if err := dec.DecodeElement(&s, &start); err != nil {
				t.Fatal(err)
			}
			return s
		case "true", "false":
			_ = dec.Skip()
			return start.Name.Local == "true"
		case "array":
			var out []string
			for {
				tok, err := dec.Token()
				if err != nil {
					t.Fatal(err)
				}
				switch el := tok.(type) {
				case xml.StartElement:
					var s string
					if err := dec.DecodeElement(&s, &el); err != nil {
						t.Fatal(err)
					}
					out = append(out, s)
				case xml.EndElement:
					return out
				}
			}
		case "dict":
			return dict()
		}
		t.Fatalf("unexpected plist element %q", start.Name.Local)
		return nil
	}
	for {
		tok, err := dec.Token()
		if err != nil {
			t.Fatalf("no <dict> in plist: %v", err)
		}
		if el, ok := tok.(xml.StartElement); ok && el.Name.Local == "dict" {
			return dict()
		}
	}
}

func TestBundleAgentRunsTheBundledDaemonAtLoadAndKeepsItAlive(t *testing.T) {
	d := plistDict(t, BundleAgent(Profile{BundleID: ReleaseBundleID}).Plist())
	if d["Label"] != "dev.hal9001.daemon" {
		t.Errorf("Label = %v", d["Label"])
	}
	if d["BundleProgram"] != "Contents/Resources/hal9001" {
		t.Errorf("BundleProgram = %v", d["BundleProgram"])
	}
	if args, _ := d["ProgramArguments"].([]string); strings.Join(args, " ") != "hal9001 serve" {
		t.Errorf("ProgramArguments = %v (argv[0] first, then the subcommand)", d["ProgramArguments"])
	}
	if d["RunAtLoad"] != true || d["KeepAlive"] != true {
		t.Errorf("RunAtLoad = %v, KeepAlive = %v", d["RunAtLoad"], d["KeepAlive"])
	}
	if ids, _ := d["AssociatedBundleIdentifiers"].([]string); len(ids) != 1 || ids[0] != "dev.hal9001" {
		t.Errorf("AssociatedBundleIdentifiers = %v", d["AssociatedBundleIdentifiers"])
	}
	env, _ := d["EnvironmentVariables"].(map[string]any)
	if env["HAL9001_LAUNCHD"] != "1" || env["HAL9001_LOG_FILE"] != "~/Library/Logs/hal9001/hal9001.log" {
		t.Errorf("env = %v", env)
	}
	for _, k := range []string{"HAL9001_HOME", "HAL9001_DEV_PORT"} {
		if _, ok := env[k]; ok {
			t.Errorf("release agent must not pin %s", k)
		}
	}
	if _, ok := d["StandardOutPath"]; ok {
		t.Error("a bundled agent cannot name an absolute log path; the daemon opens its own log file")
	}
}

func TestDevBundleAgentIsIsolatedFromTheRealOne(t *testing.T) {
	p := Profile{BundleID: "dev.hal9001.dev", Home: "/tmp/hal home", Port: 19001}
	if !p.Dev() || p.Label() != "dev.hal9001.dev.daemon" || p.CLIName() != "hal9001-dev" {
		t.Errorf("dev profile = %+v: label %q cli %q", p, p.Label(), p.CLIName())
	}
	d := plistDict(t, BundleAgent(p).Plist())
	env, _ := d["EnvironmentVariables"].(map[string]any)
	if d["Label"] != "dev.hal9001.dev.daemon" || env["HAL9001_HOME"] != "/tmp/hal home" || env["HAL9001_DEV_PORT"] != "19001" ||
		env["HAL9001_LOG_FILE"] != "~/Library/Logs/hal9001-dev/hal9001.log" {
		t.Errorf("label %v env %v", d["Label"], env)
	}
	if p.APIURL() != "http://127.0.0.1:19001" || p.SharesDefaultDaemon() {
		t.Errorf("APIURL %s shares default %v", p.APIURL(), p.SharesDefaultDaemon())
	}
	rel := Profile{BundleID: ReleaseBundleID}
	if rel.Dev() || rel.CLIName() != "hal9001" || rel.APIURL() != "http://127.0.0.1:9001" || !rel.SharesDefaultDaemon() {
		t.Errorf("release profile = %+v", rel)
	}
	if (Profile{}).Bundled() {
		t.Error("an empty profile is not bundled")
	}
}

func TestHeadlessAgentUsesAbsolutePathsAndEscapesThem(t *testing.T) {
	a := HeadlessAgent("/opt/a&b/hal9001", "/Users/me/.hal9001", "/Users/me")
	data := a.Plist()
	d := plistDict(t, data)
	if d["Label"] != "dev.hal9001.daemon.headless" {
		t.Errorf("Label = %v", d["Label"])
	}
	if _, ok := d["BundleProgram"]; ok {
		t.Error("a headless agent has no bundle")
	}
	if args, _ := d["ProgramArguments"].([]string); strings.Join(args, " ") != "/opt/a&b/hal9001 serve --home /Users/me/.hal9001" {
		t.Errorf("ProgramArguments = %v", d["ProgramArguments"])
	}
	env, _ := d["EnvironmentVariables"].(map[string]any)
	want := "/Users/me/Library/Logs/hal9001/hal9001.log"
	if env["HAL9001_LAUNCHD"] != "1" || env["HAL9001_LOG_FILE"] != want || d["StandardOutPath"] != want || d["StandardErrorPath"] != want {
		t.Errorf("env %v, out %v, err %v", env, d["StandardOutPath"], d["StandardErrorPath"])
	}
	if d["RunAtLoad"] != true || d["KeepAlive"] != true {
		t.Errorf("RunAtLoad = %v, KeepAlive = %v", d["RunAtLoad"], d["KeepAlive"])
	}
	if !strings.Contains(string(data), "/opt/a&amp;b/hal9001") {
		t.Errorf("ampersand not escaped:\n%s", data)
	}
}

func writePlist(t *testing.T, userHome, label string) string {
	t.Helper()
	p := PlistPath(userHome, label)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("<plist/>"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDetectFindsHeadlessAndLegacyAgents(t *testing.T) {
	home := t.TempDir()
	if f, err := Detect(home); err != nil || f.Conflicts() {
		t.Fatalf("empty home: %+v, %v", f, err)
	}

	headless := writePlist(t, home, HeadlessLabel)
	f, err := Detect(home)
	if err != nil || !f.Headless || f.HeadlessPath != headless || len(f.Legacy) != 0 || !f.Conflicts() {
		t.Fatalf("headless: %+v, %v", f, err)
	}

	d := writePlist(t, home, LegacyDaemonLabel)
	m := writePlist(t, home, LegacyMenubarLabel)
	f, err = Detect(home)
	if err != nil || len(f.Legacy) != 2 || f.Legacy[0] != (Legacy{LegacyDaemonLabel, d}) || f.Legacy[1] != (Legacy{LegacyMenubarLabel, m}) {
		t.Fatalf("legacy: %+v, %v", f, err)
	}
}

type launchctlLog struct{ calls []string }

func (l *launchctlLog) run(_ context.Context, args ...string) ([]byte, error) {
	l.calls = append(l.calls, strings.Join(args, " "))
	return []byte("Boot-out failed: 3: No such process"), errors.New("exit status 3")
}

func TestRemoveLegacyBootsOutAndDeletesPlistsEvenWhenNotLoaded(t *testing.T) {
	home := t.TempDir()
	writePlist(t, home, LegacyDaemonLabel)
	writePlist(t, home, LegacyMenubarLabel)
	keep := writePlist(t, home, HeadlessLabel)
	f, _ := Detect(home)

	var lc launchctlLog
	if err := RemoveLegacy(t.Context(), lc.run, 501, f.Legacy); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(lc.calls, "; "); got != "bootout gui/501/com.hal9001.daemon; bootout gui/501/dev.hal9001.menubar" {
		t.Errorf("launchctl calls = %s", got)
	}
	after, _ := Detect(home)
	if len(after.Legacy) != 0 {
		t.Errorf("legacy plists left: %+v", after.Legacy)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("the headless plist must survive: %v", err)
	}
}

func TestInstallCLILinksAndReportsPath(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(t.TempDir(), "HAL9001.app", "Contents", "Resources", "hal9001")
	bin := filepath.Join(home, ".local", "bin")

	res, err := InstallCLI(home, "hal9001", target, "/usr/bin:/bin")
	if err != nil || !res.Changed || res.OnPath || res.Link != filepath.Join(bin, "hal9001") {
		t.Fatalf("first install: %+v, %v", res, err)
	}
	if got, _ := os.Readlink(res.Link); got != target {
		t.Errorf("link -> %q, want %q", got, target)
	}

	res, err = InstallCLI(home, "hal9001", target, "/usr/bin:"+bin)
	if err != nil || res.Changed || !res.OnPath {
		t.Errorf("same target again: %+v, %v (want unchanged, on PATH)", res, err)
	}

	other := target + "-2"
	res, err = InstallCLI(home, "hal9001", other, bin+"/")
	if err != nil || !res.Changed || !res.OnPath {
		t.Errorf("repoint: %+v, %v", res, err)
	}
	if got, _ := os.Readlink(res.Link); got != other {
		t.Errorf("link -> %q, want %q", got, other)
	}
}

func TestInstallCLIRefusesToOverwriteARegularFile(t *testing.T) {
	home := t.TempDir()
	link := filepath.Join(CLIDir(home), "hal9001")
	if err := os.MkdirAll(filepath.Dir(link), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(link, []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallCLI(home, "hal9001", "/x/hal9001", ""); err == nil || !strings.Contains(err.Error(), "not a symlink") {
		t.Fatalf("err = %v", err)
	}
	if b, _ := os.ReadFile(link); string(b) != "mine" {
		t.Errorf("file was overwritten: %q", b)
	}
}

func TestParsePrintReadsTheJobNotItsCoalitions(t *testing.T) {
	out := "gui/502/dev.hal9001.daemon = {\n\tactive count = 1\n\tstate = running\n\n\tenvironment = {\n\t\tstate => nope\n\t}\n\truns = 3\n\tpid = 48135\n\tlast exit code = 1\n\n\tresource coalition = {\n\t\tstate = active\n\t}\n}\n"
	got := ParsePrint(out)
	if got != (PrintInfo{State: "running", PID: 48135, Runs: 3, LastExit: "1"}) {
		t.Errorf("ParsePrint = %+v", got)
	}
	if got := ParsePrint(""); got != (PrintInfo{}) {
		t.Errorf("empty output: %+v", got)
	}
}

func TestExpandTilde(t *testing.T) {
	for in, want := range map[string]string{"~/a/b.log": "/u/a/b.log", "~": "/u", "/abs/x": "/abs/x", "~x/y": "~x/y"} {
		if got := ExpandTilde(in, "/u"); got != want {
			t.Errorf("ExpandTilde(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRelevantIgnoresOldAgentsForADevBuildOnItsOwnPort(t *testing.T) {
	found := Found{Headless: true, Legacy: []Legacy{{LegacyDaemonLabel, "/x"}}}
	if got := (Profile{BundleID: ReleaseBundleID}).Relevant(found); !got.Conflicts() || !got.Headless || len(got.Legacy) != 1 {
		t.Errorf("a profile on the default port competes with them: %+v", got)
	}
	if got := (Profile{BundleID: "dev.hal9001.dev", Port: 19001}).Relevant(found); got.Conflicts() {
		t.Errorf("a dev build on port 19001 cannot conflict with agents bound to 9001: %+v", got)
	}
	if got := (Profile{BundleID: "dev.hal9001.dev", Port: 9001}).Relevant(found); !got.Conflicts() {
		t.Errorf("a dev build pinned to the default port does conflict: %+v", got)
	}
}
