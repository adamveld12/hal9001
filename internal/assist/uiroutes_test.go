package assist

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func TestCleanUIPathAcceptsTheAppsPagesAndNormalisesThem(t *testing.T) {
	for in, want := range map[string]string{
		"/inbox":                            "/inbox",
		"/inbox/":                           "/inbox",
		"/inbox/01JC7Z3K9Q8V5T2M4N6P8R0S1T": "/inbox/01JC7Z3K9Q8V5T2M4N6P8R0S1T",
		"/inbox/approval/01JC7Z3K9Q8V5T2M4N6P8R0S1T?kind=action": "/inbox/approval/01JC7Z3K9Q8V5T2M4N6P8R0S1T?kind=action",
		"/flows/triage":                                   "/flows/triage",
		"/flows?view=runs&status=failed&flow=x":           "/flows?flow=x&status=failed&view=runs",
		"/flows/triage/versions/ab12cd34":                 "/flows/triage/versions/ab12cd34",
		"/settings/runtimes":                              "/settings/runtimes",
		"/sandboxes/docker":                               "/sandboxes/docker",
		"/prompts/my.prompt_v2-x?tab=preview":             "/prompts/my.prompt_v2-x?tab=preview",
		"/timeline?q=out+of+memory":                       "/timeline?q=out+of+memory",
		"/timeline?q=a%26b%3Dc":                           "/timeline?q=a%26b%3Dc",
		"/runs/01JC7Z3K9Q8V5T2M4N6P8R0S1T?run=r1&node=n1": "/runs/01JC7Z3K9Q8V5T2M4N6P8R0S1T?node=n1&run=r1",
		"/chat": "/chat",
		"/chat?status=awaiting_approval&source=flow&q=out+of+memory&since=2026-11-01": "/chat?q=out+of+memory&since=2026-11-01&source=flow&status=awaiting_approval",
		"/chat/01JC7Z3K9Q8V5T2M4N6P8R0S1T?flow=triage":                                "/chat/01JC7Z3K9Q8V5T2M4N6P8R0S1T?flow=triage",
		"/chat/new": "/chat/new",
	} {
		got, err := cleanUIPath(in)
		if err != nil || got != want {
			t.Errorf("cleanUIPath(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestCleanUIPathRefusesAnythingButAnInAppPageOfTheCatalog(t *testing.T) {
	for _, in := range []string{
		"",
		"inbox",
		"https://evil.example/inbox",
		"http://localhost:9001/inbox",
		"//evil.example/inbox",
		"//inbox",
		"javascript:alert(1)",
		"/javascript:alert(1)",
		"data:text/html,x",
		"/\\evil.example",
		"/inbox\\..\\x",
		"/inbox\n",
		"/inbox\x00",
		"/inbox\t/x",
		"/inbox#frag",
		"/inbox?q=a#frag",
		"/flows/../settings/general",
		"/flows/./triage",
		"/flows/..",
		"/flows/%2e%2e",
		"/flows/a%2Fb",
		"/flows/a b",
		"/flows/triage/extra",
		"/flows/triage/versions",
		"/nope",
		"/",
		"/settings",
		"/approvals/01JC7Z3K9Q8V5T2M4N6P8R0S1T", // redirect-only in the app, not offered
		"/flows/triage?view=runs",               // /flows/:id takes no query
		"/inbox?next=https://evil.example",
		"/inbox?q=a&redirect=/x",
		"/chat?assist=01JC7Z3K9Q8V5T2M4N6P8R0S1T", // the drawer's link key, not a filter of the page
		"/chat/01JC7Z3K9Q8V5T2M4N6P8R0S1T/extra",
		"/inbox?=x",
		"/inbox?q=a%0Ab",
		"/inbox?q=%zz",
		"/inbox?q=" + strings.Repeat("x", maxUIQueryValue+1),
		"/inbox/" + strings.Repeat("a", 129),
		"/" + strings.Repeat("a", maxUIPath),
	} {
		if got, err := cleanUIPath(in); err == nil {
			t.Errorf("cleanUIPath(%q) = %q, want an error", in, got)
		}
	}
}

// The catalog is the assistant's only list of pages, so it must be exactly the app's routes (web/src/App.tsx), minus the
// ones that only redirect.
func TestRouteCatalogMatchesTheWebApp(t *testing.T) {
	src, err := os.ReadFile("../../web/src/App.tsx")
	if err != nil {
		t.Skipf("web/src/App.tsx not available: %v", err)
	}
	app := map[string]bool{}
	for _, m := range regexp.MustCompile(`\bpath="([^"]+)"`).FindAllStringSubmatch(string(src), -1) {
		if m[1] != "*" {
			app["/"+strings.TrimPrefix(m[1], "/")] = true
		}
	}
	if len(app) < 10 {
		t.Fatalf("found only %d routes in App.tsx: %v (did its route syntax change?)", len(app), app)
	}
	redirectOnly := map[string]bool{"/settings": true, "/settings/tailscale": true, "/approvals/:id": true}
	catalog := map[string]bool{}
	for _, r := range uiRoutes {
		catalog[r.Pattern] = true
		if !app[r.Pattern] {
			t.Errorf("catalog route %s is not a route of web/src/App.tsx", r.Pattern)
		}
	}
	var missing []string
	for p := range app {
		if !catalog[p] && !redirectOnly[p] {
			missing = append(missing, p)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("routes of web/src/App.tsx missing from the catalog (uiroutes.go): %v", missing)
	}
	for p := range redirectOnly {
		if !app[p] || catalog[p] {
			t.Errorf("redirect-only route %s: in App.tsx %v, in the catalog %v; update this test", p, app[p], catalog[p])
		}
	}
}

func TestUIGuideListsEveryRouteWithItsQueryKeys(t *testing.T) {
	g := uiGuide()
	for _, r := range uiRoutes {
		if !strings.Contains(g, "`"+r.Pattern+"`") {
			t.Errorf("the guide does not list %s", r.Pattern)
		}
		for _, q := range r.Query {
			if !strings.Contains(g, "`"+q.Key+"`") {
				t.Errorf("the guide does not list the query key %s of %s", q.Key, r.Pattern)
			}
		}
	}
}
