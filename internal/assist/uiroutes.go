package assist

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// The web app's pages as the assistant may open them (the navigate_ui tool). This is the single source of the routes
// the assistant knows: a path outside it is refused, so a model (or text it was fed) cannot send the user's browser
// anywhere else. uiroutes_test.go compares the catalog with web/src/App.tsx so the two cannot drift.

// maxUIPath bounds the whole path with its query.
const maxUIPath = 512

// maxUIQueryValue bounds one query value.
const maxUIQueryValue = 200

// uiParamRE is the shape of a :param segment: an id, a name, a hash or a ULID.
var uiParamRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// uiRoute is one page: its pattern (":name" segments are parameters), what it shows and the query keys it reads.
type uiRoute struct {
	Pattern string
	Desc    string
	// Query lists the allowed query keys, with the values the page understands.
	Query []uiQuery
}

type uiQuery struct{ Key, Doc string }

// inboxQuery is the query of the inbox routes; an item opens in the same page, which reads the same filters.
var inboxQuery = []uiQuery{
	{"kind", "action|notes"}, {"unread", "1"}, {"starred", "1"}, {"archived", "1"}, {"priority", "the priority name"},
	{"dry", "only|hide"}, {"q", "search text"}, {"sort", "time"},
}

// chatQuery is the query of the chat routes; a conversation opens in the same page, which reads the same filters.
var chatQuery = []uiQuery{
	{"source", "flow|chat"}, {"flow", "flow id"}, {"runtime", "runtime name"},
	{"status", "a conversation status: running, awaiting_approval, idle, succeeded, failed, killed, denied, interrupted, timeout, closed"},
	{"since", "a day, YYYY-MM-DD"}, {"until", "a day, YYYY-MM-DD"}, {"q", "search text"},
}

// uiRoutes is the route catalog, in the order the guide lists it.
var uiRoutes = []uiRoute{
	{"/inbox", "the inbox: items that need the user's attention, and notes", inboxQuery},
	{"/inbox/:id", "one inbox item (:id is the inbox item id) in the inbox", inboxQuery},
	{"/inbox/approval/:id", "one approval (:id is the approval id) in the inbox", inboxQuery},
	{"/approvals", "the approval queue and its history", []uiQuery{{"tab", "history"}, {"status", "approved|denied|expired, with tab=history"}, {"type", "proposal|tool_call|request"}}},
	{"/timeline", "events, newest first, with what the flows decided", []uiQuery{
		{"sensor", "sensor id"}, {"flow", "flow id"}, {"status", "a run status"}, {"decision", "a flow decision"}, {"kind", "a run kind"}, {"q", "search text"},
	}},
	{"/events/:id", "one event with its fan-out (:id is the event id)", nil},
	{"/runs/:id", "one flow run with its node runs (:id is the flow run id)", []uiQuery{{"node", "node id to select"}, {"run", "node run id to select"}}},
	{"/agent-runs", "agent runs", []uiQuery{{"status", "an agent run status"}, {"runtime", "runtime name"}}},
	{"/agent-runs/:id", "one agent run with its transcript (:id is the agent run id)", nil},
	{"/flows", "the flows list, or the run list (view=runs) or the queue (view=queue)", []uiQuery{
		{"view", "runs|queue"}, {"flow", "flow id, with view=runs"}, {"status", "a run status, with view=runs"}, {"kind", "a run kind, with view=runs"},
	}},
	{"/flows/:id", "the editor of one flow (:id is the flow id)", nil},
	{"/flows/:id/versions/:hash", "one stored version of a flow (:hash is the version hash)", []uiQuery{{"tab", "yaml"}}},
	{"/sensors", "the sensors list", []uiQuery{{"new", "1: start a new sensor"}}},
	{"/sensors/:id", "the editor of one sensor (:id is the sensor id)", nil},
	{"/tools", "the custom tools list", []uiQuery{{"new", "1: start a new tool"}}},
	{"/tools/:id", "the editor of one custom tool (:id is the tool id)", nil},
	{"/prompts", "the prompt files list", []uiQuery{{"new", "1: start a new prompt"}}},
	{"/prompts/:name", "the editor of one prompt (:name is the prompt name)", []uiQuery{{"tab", "preview"}, {"sample", "json"}, {"event", "event id, for the preview"}}},
	{"/chat", "the chat page: every conversation (chats and the agent runs of flow nodes) in a list with filters", chatQuery},
	{"/chat/:id", "one conversation in the chat page (:id is the assist session id of a chat, or the agent run id of a flow run; new starts a new chat)", chatQuery},
	{"/settings/general", "settings: general", nil},
	{"/settings/environment", "settings: environment variables (names only)", nil},
	{"/settings/context", "settings: the context text every agent gets", nil},
	{"/settings/runtimes", "settings: agent runtimes", nil},
	{"/settings/auth", "settings: sign-in and access", nil},
	{"/settings/tailscale", "settings: share hal on your tailnet (Tailscale)", nil},
	{"/settings/mcp", "settings: hal's MCP server", nil},
	{"/settings/updates", "settings: updates", nil},
	{"/sandboxes", "the sandbox backends", nil},
	{"/sandboxes/:backend", "one sandbox backend (:backend is local, docker, depot, kubernetes or ssh)", nil},
}

// uiPathError is why navigate_ui refused a path.
func uiPathError(raw, format string, args ...any) error {
	return fmt.Errorf("navigate_ui: path %q %s", shorten(raw, 80), fmt.Sprintf(format, args...))
}

// cleanUIPath validates path against the route catalog and returns it normalised: no trailing slash, the query keys
// sorted and percent-encoded. It accepts only an in-app path ("/flows/triage?view=runs"): never a scheme, a host, "//",
// a backslash, a control character, a fragment, a ".." segment, an unknown route or an unknown query key.
func cleanUIPath(raw string) (string, error) {
	switch {
	case raw == "":
		return "", uiPathError(raw, "is empty")
	case len(raw) > maxUIPath:
		return "", uiPathError(raw, "is longer than %d bytes", maxUIPath)
	case raw[0] != '/':
		return "", uiPathError(raw, "must be an in-app path starting with /")
	case strings.HasPrefix(raw, "//"):
		return "", uiPathError(raw, "must not start with //")
	}
	for _, r := range raw {
		switch {
		case unicode.IsControl(r):
			return "", uiPathError(raw, "contains a control character")
		case r == '\\':
			return "", uiPathError(raw, "contains a backslash")
		case r == '#':
			return "", uiPathError(raw, "must not have a fragment")
		}
	}
	p, q, _ := strings.Cut(raw, "?")
	if p != "/" {
		p = strings.TrimSuffix(p, "/")
	}
	route, ok := matchUIRoute(p)
	if !ok {
		return "", uiPathError(raw, "is not a page of the app (the pages are listed in your instructions)")
	}
	if q == "" {
		return p, nil
	}
	vals, err := url.ParseQuery(q)
	if err != nil {
		return "", uiPathError(raw, "has a malformed query")
	}
	allowed := map[string]bool{}
	for _, k := range route.Query {
		allowed[k.Key] = true
	}
	for k, vs := range vals {
		if !allowed[k] {
			return "", uiPathError(raw, "uses the query key %q, which %s does not take (allowed: %s)", k, route.Pattern, queryKeys(route))
		}
		for _, v := range vs {
			if len(v) > maxUIQueryValue || strings.IndexFunc(v, unicode.IsControl) >= 0 {
				return "", uiPathError(raw, "has a query value of %q that is too long or has control characters", k)
			}
		}
	}
	if enc := vals.Encode(); enc != "" {
		return p + "?" + enc, nil
	}
	return p, nil
}

func queryKeys(r uiRoute) string {
	if len(r.Query) == 0 {
		return "none"
	}
	keys := make([]string, len(r.Query))
	for i, k := range r.Query {
		keys[i] = k.Key
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}

// matchUIRoute finds the catalog route a path (without query) is an instance of.
func matchUIRoute(p string) (uiRoute, bool) {
	segs := strings.Split(strings.TrimPrefix(p, "/"), "/")
next:
	for _, r := range uiRoutes {
		pat := strings.Split(strings.TrimPrefix(r.Pattern, "/"), "/")
		if len(pat) != len(segs) {
			continue
		}
		for i, ps := range pat {
			if strings.HasPrefix(ps, ":") {
				if !uiParamRE.MatchString(segs[i]) || segs[i] == "." || segs[i] == ".." {
					continue next
				}
			} else if ps != segs[i] {
				continue next
			}
		}
		return r, true
	}
	return uiRoute{}, false
}

// uiGuide is the instructions section about the app's pages: what navigate_ui can open, shared by every session kind.
func uiGuide() string {
	var b strings.Builder
	b.WriteString("## The app's pages (navigate_ui and start_draft)\n\n")
	b.WriteString("`navigate_ui` with `path` (and an optional one-line `reason`) opens a page of the app for the user. It moves the user's view only: it changes nothing and needs no approval. ")
	b.WriteString("Use it when the user asks to see or open something, or when the answer is easiest to read on a page: first find the id with the read tools, then open the page. ")
	b.WriteString("Do not navigate just to look something up (use the read tools) and never more than once per answer; say in your reply where you took the user. ")
	b.WriteString("The path must be one of these, with every `:name` replaced by an id (letters, digits, `.`, `_`, `-`); other paths and query keys are refused. The result only says the view was asked to open the page: you cannot see the page.\n\n")
	var prev []uiQuery
	for _, r := range uiRoutes {
		fmt.Fprintf(&b, "- `%s`: %s.", r.Pattern, r.Desc)
		if len(r.Query) > 0 && len(prev) == len(r.Query) && &prev[0] == &r.Query[0] {
			b.WriteString(" Query: the same as the page above.")
		} else if len(r.Query) > 0 {
			b.WriteString(" Query:")
			for i, k := range r.Query {
				sep := ","
				if i == 0 {
					sep = ""
				}
				fmt.Fprintf(&b, "%s `%s` (%s)", sep, k.Key, k.Doc)
			}
			b.WriteString(".")
		}
		b.WriteString("\n")
		prev = r.Query
	}
	b.WriteString("\n`start_draft` with `kind` (`sensor`, `flow` or `tool`), an optional `id` and an optional `prompt` offers the user a new draft session on that target: the chat switches to it and `prompt` becomes its first message. ")
	b.WriteString("`id` must be an existing sensor, flow or tool (omit it to start a new one). It needs no approval and changes nothing by itself; the draft session is where the work is done and tested.\n\n")
	return b.String()
}
