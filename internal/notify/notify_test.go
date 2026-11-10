package notify_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os/exec"
	"reflect"
	"slices"
	"strings"
	"testing"

	"hal9001/internal/core"
	"hal9001/internal/notify"
)

type call struct {
	name string
	args []string
}

type fakeSystem struct {
	have  map[string]bool  // executables found on PATH
	fail  map[string]error // per-executable run error
	calls []call
}

func (f *fakeSystem) look(name string) (string, error) {
	if !f.have[name] {
		return "", exec.ErrNotFound
	}
	return "/usr/local/bin/" + name, nil
}

func (f *fakeSystem) run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, call{name, args})
	base := name[strings.LastIndex(name, "/")+1:]
	if err := f.fail[base]; err != nil {
		return []byte("boom"), err
	}
	return nil, nil
}

func (f *fakeSystem) notifier() core.Notifier {
	return notify.New(notify.WithRunner(f.run), notify.WithLookPath(f.look),
		notify.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
}

var inbox = core.Notification{
	Kind: "note", Title: "triage", Subtitle: "Large PR needs you", Body: "babylist/web#4120", ThreadID: "triage",
	Priority: "high", URL: "http://localhost:9001/inbox/01J",
}

func TestTerminalNotifierGetsRichFields(t *testing.T) {
	f := &fakeSystem{have: map[string]bool{"terminal-notifier": true, "osascript": true}}
	n := notify.New(notify.WithRunner(f.run), notify.WithLookPath(f.look), notify.WithIcon("/home/data/hal-eye.png"),
		notify.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err := n.Notify(context.Background(), inbox); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 || f.calls[0].name != "/usr/local/bin/terminal-notifier" {
		t.Fatalf("calls = %v, want only terminal-notifier", f.calls)
	}
	want := []string{
		"-title", "triage", "-subtitle", "Large PR needs you", "-message", "babylist/web#4120", "-group", "hal9001:triage",
		"-sound", "default", "-contentImage", "/home/data/hal-eye.png", "-open", "http://localhost:9001/inbox/01J",
	}
	if !reflect.DeepEqual(f.calls[0].args, want) {
		t.Errorf("args = %q\nwant %q", f.calls[0].args, want)
	}
}

func TestNormalPriorityHasNoSoundAndEmptyBodyUsesSubtitle(t *testing.T) {
	args := notify.TerminalNotifierArgs(core.Notification{Title: "flow", Subtitle: "-1 approval pending", Priority: "normal"}, "")
	if !slices.Contains(args, "-message") || args[slices.Index(args, "-message")+1] != " -1 approval pending" {
		t.Errorf("args = %q; want message = subtitle guarded against a leading dash", args)
	}
	for _, a := range args {
		if a == "-sound" || a == "-open" || a == "-contentImage" {
			t.Errorf("unexpected %s in %q", a, args)
		}
	}
}

func TestFallsBackToOsascriptWhenTerminalNotifierIsMissing(t *testing.T) {
	f := &fakeSystem{have: map[string]bool{"osascript": true}}
	n := core.Notification{Title: `He said "hi"`, Body: `it's \ fine`, URL: "http://localhost:9001/inbox/1"}
	if err := f.notifier().Notify(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 || !strings.HasSuffix(f.calls[0].name, "osascript") {
		t.Fatalf("calls = %v, want osascript", f.calls)
	}
	args := f.calls[0].args
	// The text is passed after "--" as argv items; the script never contains it.
	script := strings.Join(args[:len(args)-4], " ")
	if strings.Contains(script, "hi") || strings.Contains(script, "fine") {
		t.Errorf("notification text was spliced into the script: %q", script)
	}
	if tail := args[len(args)-4:]; tail[0] != "--" || tail[1] != n.Body || tail[2] != n.Title || tail[3] != n.Subtitle {
		t.Errorf("argv tail = %q, want [-- body title subtitle]", tail)
	}
}

func TestFallsBackToOsascriptWhenTerminalNotifierFails(t *testing.T) {
	f := &fakeSystem{
		have: map[string]bool{"terminal-notifier": true, "osascript": true},
		fail: map[string]error{"terminal-notifier": errors.New("exit status 1")},
	}
	if err := f.notifier().Notify(context.Background(), inbox); err != nil {
		t.Fatalf("Notify = %v, want nil after fallback", err)
	}
	if len(f.calls) != 2 || !strings.HasSuffix(f.calls[1].name, "osascript") {
		t.Errorf("calls = %v, want terminal-notifier then osascript", f.calls)
	}
}

func TestBothFailingReturnsAnErrorNamingBoth(t *testing.T) {
	f := &fakeSystem{
		have: map[string]bool{"terminal-notifier": true, "osascript": true},
		fail: map[string]error{"terminal-notifier": errors.New("tn broke"), "osascript": errors.New("osa broke")},
	}
	err := f.notifier().Notify(context.Background(), inbox)
	if err == nil || !strings.Contains(err.Error(), "tn broke") || !strings.Contains(err.Error(), "osa broke") {
		t.Errorf("Notify = %v, want an error mentioning both failures", err)
	}
}

func TestNothingInstalledIsAnErrorNotAPanic(t *testing.T) {
	f := &fakeSystem{have: map[string]bool{}}
	if err := f.notifier().Notify(context.Background(), inbox); err == nil {
		t.Error("Notify succeeded with neither tool installed")
	}
}
