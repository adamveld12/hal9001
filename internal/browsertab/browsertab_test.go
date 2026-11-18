package browsertab

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSameSite(t *testing.T) {
	const ui = "http://localhost:9001"
	for _, tc := range []struct {
		name string
		url  string
		want bool
	}{
		{"the origin itself", "http://localhost:9001", true},
		{"a deep link", "http://localhost:9001/approvals/abc?x=1#top", true},
		{"scheme and host are case-insensitive", "HTTP://LocalHost:9001/inbox", true},
		{"loopback address of the same port", "http://127.0.0.1:9001/inbox", true},
		{"another localhost port", "http://localhost:5173/inbox", false},
		{"a port that only starts the same", "http://localhost:90010/inbox", false},
		{"no port is port 80", "http://localhost/inbox", false},
		{"another scheme", "https://localhost:9001/inbox", false},
		{"host that merely contains it", "http://localhost:9001.evil.test/", false},
		{"userinfo cannot spoof the host", "http://localhost:9001@evil.test/", false},
		{"another site on the same port", "http://example.com:9001/", false},
		{"chrome's new tab page", "chrome://newtab/", false},
		{"not a URL", "", false},
	} {
		if got := SameSite(tc.url, ui); got != tc.want {
			t.Errorf("%s: SameSite(%q, %q) = %v, want %v", tc.name, tc.url, ui, got, tc.want)
		}
	}
	if !SameSite("https://hal.example.ts.net/x", "https://hal.example.ts.net:443") {
		t.Error("an explicit default port must equal the implicit one")
	}
	if SameSite("http://example.com:9001/", "http://localhost:9001") || SameSite("http://127.0.0.1:9001", "http://example.com:9001") {
		t.Error("loopback names only equal each other")
	}
}

func TestPickPrefersTheFrontmostActiveTab(t *testing.T) {
	const link = "http://localhost:9001/approvals/1"
	tabs := []Tab{
		{Window: "1", ID: "10", Frontmost: true, URL: "https://github.com/x"},
		{Window: "1", ID: "11", Frontmost: true, URL: "http://localhost:5173/"},
		{Window: "1", ID: "12", Frontmost: true, Active: true, URL: "http://localhost:9001/inbox"},
		{Window: "1", ID: "13", Frontmost: true, URL: "http://localhost:9001/flows"},
		{Window: "2", ID: "20", Active: true, URL: "http://localhost:9001/runs"},
	}
	if got, ok := pick(tabs, link); !ok || got.ID != "12" {
		t.Errorf("got %+v, %v; want the frontmost window's active tab 12", got, ok)
	}
	// The frontmost window's active tab is not hal9001: the first match wins, even if another window's active tab matches.
	tabs[2].URL = "https://example.com/"
	if got, ok := pick(tabs, link); !ok || got.ID != "13" {
		t.Errorf("got %+v, %v; want first match 13", got, ok)
	}
	if _, ok := pick(tabs[:2], link); ok {
		t.Error("no hal9001 tab must not match")
	}
}

type fakeBrowser struct {
	running   bool
	tabs      []Tab
	tabsErr   error
	showErr   error
	block     bool // Tabs waits for ctx, like a permission prompt nobody answers
	listed    int
	shown     []Tab
	shownURLs []string
}

func (f *fakeBrowser) Name() string  { return "fake" }
func (f *fakeBrowser) Running() bool { return f.running }
func (f *fakeBrowser) Tabs(ctx context.Context) ([]Tab, error) {
	f.listed++
	if f.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return f.tabs, f.tabsErr
}
func (f *fakeBrowser) Show(_ context.Context, t Tab, u string) error {
	f.shown, f.shownURLs = append(f.shown, t), append(f.shownURLs, u)
	return f.showErr
}

func TestOpenReusesATabOrFallsBack(t *testing.T) {
	const link = "http://localhost:9001/approvals/1"
	hal := []Tab{{Window: "1", ID: "5", Frontmost: true, Active: true, URL: "http://localhost:9001/inbox"}}
	for _, tc := range []struct {
		name         string
		b            fakeBrowser
		link         string
		wantFallback bool
		wantListed   int
	}{
		{"reuses the tab", fakeBrowser{running: true, tabs: hal}, link, false, 1},
		{"browser not running: never asked", fakeBrowser{running: false, tabs: hal}, link, true, 0},
		{"no hal9001 tab", fakeBrowser{running: true, tabs: []Tab{{Window: "1", ID: "1", URL: "https://github.com"}}}, link, true, 1},
		{"no tabs at all", fakeBrowser{running: true}, link, true, 1},
		{"permission denied or script error", fakeBrowser{running: true, tabsErr: errors.New("Not authorized to send Apple events (-1743)")}, link, true, 1},
		{"tab closed before it was shown", fakeBrowser{running: true, tabs: hal, showErr: errors.New("gone")}, link, true, 1},
		{"link is not on the UI: browser never asked", fakeBrowser{running: true, tabs: hal}, "https://github.com/adamveld12/hal9001", true, 0},
		{"link on another localhost port: browser never asked", fakeBrowser{running: true, tabs: hal}, "http://localhost:5173/", true, 0},
	} {
		var fell []string
		o := Opener{UI: "http://127.0.0.1:9001", Browsers: []Browser{&tc.b}, Fallback: func(u string) error { fell = append(fell, u); return nil }}
		if err := o.Open(tc.link); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if (len(fell) == 1) != tc.wantFallback || len(fell) > 1 || (len(fell) == 1 && fell[0] != tc.link) {
			t.Errorf("%s: fallback calls %v, want fallback=%v", tc.name, fell, tc.wantFallback)
		}
		if tc.b.listed != tc.wantListed {
			t.Errorf("%s: browser listed %d times, want %d", tc.name, tc.b.listed, tc.wantListed)
		}
		if !tc.wantFallback && (len(tc.b.shown) != 1 || tc.b.shown[0].ID != "5" || tc.b.shownURLs[0] != tc.link) {
			t.Errorf("%s: shown %v %v, want tab 5 with the deep link", tc.name, tc.b.shown, tc.b.shownURLs)
		}
	}
}

func TestOpenFallsBackWhenTheBrowserHangs(t *testing.T) {
	b := &fakeBrowser{running: true, block: true}
	fell := 0
	o := Opener{UI: "http://localhost:9001", Browsers: []Browser{b}, Fallback: func(string) error { fell++; return nil }}
	start := time.Now()
	if err := o.Open("http://localhost:9001/x"); err != nil {
		t.Fatal(err)
	}
	if fell != 1 {
		t.Errorf("fallback ran %d times, want 1", fell)
	}
	if d := time.Since(start); d > Timeout+time.Second {
		t.Errorf("took %v, want about %v", d, Timeout)
	}
}

func TestOpenReturnsTheFallbackError(t *testing.T) {
	want := errors.New("open failed")
	o := Opener{UI: "http://localhost:9001", Fallback: func(string) error { return want }}
	if err := o.Open("http://localhost:9001/x"); !errors.Is(err, want) {
		t.Errorf("got %v, want the fallback's error", err)
	}
}

// recorder is a Runner that answers with canned output and keeps what it was asked.
type recorder struct {
	out     map[string]string // script text -> output
	err     error
	scripts []string
	args    [][]string
}

func (r *recorder) run(_ context.Context, script string, args ...string) (string, error) {
	r.scripts, r.args = append(r.scripts, script), append(r.args, args)
	return r.out[script], r.err
}

func TestChromePassesURLsAsArgvNeverInScriptText(t *testing.T) {
	const link = `http://localhost:9001/approvals/1?q="quoted"&x=\n`
	r := &recorder{out: map[string]string{
		listScript: "7\t42\t1\thttp://localhost:9001/inbox\n7\t43\t0\thttps://example.com/\n9\t50\t1\thttp://localhost:9001/runs\n",
		showScript: "ok",
	}}
	c := Chrome{IsRunning: func() bool { return true }, Run: r.run}
	o := Opener{UI: "http://localhost:9001", Browsers: []Browser{c}, Fallback: func(string) error { t.Fatal("fell back"); return nil }}
	if err := o.Open(link); err != nil {
		t.Fatal(err)
	}
	if len(r.scripts) != 2 {
		t.Fatalf("ran %d scripts, want list then show", len(r.scripts))
	}
	for _, s := range r.scripts {
		if strings.Contains(s, "localhost") || strings.Contains(s, "quoted") {
			t.Errorf("a URL leaked into the script text:\n%s", s)
		}
	}
	if got := strings.Join(r.args[1], "|"); got != "7|42|"+link {
		t.Errorf("show argv = %q, want window|tab|link", got)
	}
}

func TestChromeShowReportsAClosedTab(t *testing.T) {
	r := &recorder{out: map[string]string{showScript: "gone"}}
	if err := (Chrome{Run: r.run}).Show(context.Background(), Tab{Window: "1", ID: "2"}, "http://localhost:9001/"); err == nil {
		t.Error("want an error when the script says the tab is gone")
	}
}

func TestParseTabs(t *testing.T) {
	tabs, err := parseTabs("7\t42\t1\thttp://a/\n7\t43\t0\thttp://b/\n9\t50\t1\thttp://c/\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(tabs) != 3 || !tabs[0].Frontmost || !tabs[1].Frontmost || tabs[2].Frontmost || !tabs[0].Active || tabs[1].Active || tabs[2].URL != "http://c/" {
		t.Errorf("unexpected tabs %+v", tabs)
	}
	if tabs, err := parseTabs(""); err != nil || len(tabs) != 0 {
		t.Errorf("empty output: %v %v", tabs, err)
	}
	if _, err := parseTabs("garbage\n"); err == nil {
		t.Error("a malformed line must be an error, so the open falls back")
	}
}
