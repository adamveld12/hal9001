package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// github_issue_link builds a link that opens GitHub's new-issue page with the form filled in, using the URL query
// parameters of
// https://docs.github.com/en/issues/tracking-your-work-with-issues/using-issues/creating-an-issue#creating-an-issue-from-a-url-query
// It never calls GitHub: nothing exists until the user clicks the link, reviews the form and submits it.

// Limits of github_issue_link.
const (
	// DefaultIssueRepo is where hal9001 bugs go.
	DefaultIssueRepo = "adamveld12/hal9001"
	// MaxIssueURL bounds the whole link. GitHub answers 414 above its server limit, which is about 8 KB.
	MaxIssueURL = 8000
	// MaxIssueBody bounds the body the assistant passes (the full text is also returned to the UI for copying).
	MaxIssueBody = 10000
	// IssueTruncatedNote ends the body of a link whose body had to be cut to fit MaxIssueURL.
	IssueTruncatedNote = "(truncated — paste the full text below)"

	maxIssueTitle     = 256 // GitHub's own title limit
	maxIssueLabels    = 10
	maxIssueAssignees = 10
	maxIssueName      = 100 // label, milestone, template and type names
)

var (
	issueOwnerRE    = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
	issueRepoNameRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
	issueUserRE     = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
	issueTemplateRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._ -]{0,99}$`)
)

// IssueLinkParams are the fields of a new issue.
type IssueLinkParams struct {
	// Repo is "owner/name"; empty means DefaultIssueRepo.
	Repo      string
	Title     string
	Body      string
	Labels    []string
	Assignees []string
	Milestone string
	// Template is the file name of an issue template of the repository (for example bug_report.md).
	Template string
	// Type is the issue type (for example Bug). GitHub applies it only where the repository has issue types.
	Type string
}

// IssueLink is a built link.
type IssueLink struct {
	URL   string `json:"url"`
	Repo  string `json:"repo"`
	Title string `json:"title"`
	// Body is the complete body, whether or not it fits the link.
	Body string `json:"body"`
	// Truncated reports that the link carries only the start of Body, followed by IssueTruncatedNote.
	Truncated bool `json:"truncated"`
}

// ValidIssueRepo reports whether s is an "owner/name" GitHub repository.
func ValidIssueRepo(s string) bool {
	owner, name, ok := strings.Cut(s, "/")
	return ok && issueOwnerRE.MatchString(owner) && issueRepoNameRE.MatchString(name) && name != "." && name != ".."
}

// BuildIssueLink validates p and returns the new-issue link. A body that makes the link longer than MaxIssueURL is cut
// at a character boundary and ends with IssueTruncatedNote; IssueLink.Body keeps the whole text.
func BuildIssueLink(p IssueLinkParams) (IssueLink, error) {
	repo := strings.TrimSpace(p.Repo)
	if repo == "" {
		repo = DefaultIssueRepo
	}
	if !ValidIssueRepo(repo) {
		return IssueLink{}, fmt.Errorf("repo %q is not a GitHub repository: use owner/name", p.Repo)
	}
	title := oneLine(p.Title)
	switch {
	case title == "":
		return IssueLink{}, errors.New("title is required")
	case utf8.RuneCountInString(title) > maxIssueTitle:
		return IssueLink{}, fmt.Errorf("title is longer than %d characters", maxIssueTitle)
	}
	if len(p.Body) > MaxIssueBody {
		return IssueLink{}, fmt.Errorf("body is longer than %d bytes: shorten it", MaxIssueBody)
	}
	if !utf8.ValidString(p.Body) || !utf8.ValidString(title) {
		return IssueLink{}, errors.New("title and body must be valid UTF-8")
	}
	labels, err := cleanList("labels", p.Labels, maxIssueLabels, func(s string) error {
		if strings.Contains(s, ",") {
			return errors.New("must not contain a comma")
		}
		return nameOK(s)
	})
	if err != nil {
		return IssueLink{}, err
	}
	assignees, err := cleanList("assignees", p.Assignees, maxIssueAssignees, func(s string) error {
		if !issueUserRE.MatchString(strings.TrimPrefix(s, "@")) {
			return errors.New("is not a GitHub username")
		}
		return nil
	})
	if err != nil {
		return IssueLink{}, err
	}
	for i, a := range assignees {
		assignees[i] = strings.TrimPrefix(a, "@")
	}
	milestone, typ := strings.TrimSpace(p.Milestone), strings.TrimSpace(p.Type)
	for what, v := range map[string]string{"milestone": milestone, "type": typ} {
		if v == "" {
			continue
		}
		if err := nameOK(v); err != nil {
			return IssueLink{}, fmt.Errorf("%s %q %w", what, v, err)
		}
	}
	template := strings.TrimSpace(p.Template)
	if template != "" && !issueTemplateRE.MatchString(template) {
		return IssueLink{}, fmt.Errorf("template %q is not a template file name (for example bug_report.md)", p.Template)
	}

	base := "https://github.com/" + repo + "/issues/new"
	build := func(body string) string {
		var q []string
		add := func(k, v string) {
			if v != "" {
				q = append(q, k+"="+url.QueryEscape(v))
			}
		}
		add("template", template)
		add("title", title)
		add("body", body)
		add("labels", strings.Join(labels, ","))
		add("assignees", strings.Join(assignees, ","))
		add("milestone", milestone)
		add("type", typ)
		return base + "?" + strings.Join(q, "&")
	}

	link := IssueLink{Repo: repo, Title: title, Body: p.Body}
	if link.URL = build(p.Body); len(link.URL) <= MaxIssueURL {
		return link, nil
	}
	// Keep the longest prefix of the body that fits together with the note.
	runes := []rune(p.Body)
	withNote := func(n int) string { return string(runes[:n]) + "\n\n" + IssueTruncatedNote }
	if len(build(withNote(0))) > MaxIssueURL {
		return IssueLink{}, errors.New("the title and options alone make the link too long: shorten them")
	}
	lo, hi := 0, len(runes) // withNote(lo) fits; withNote(hi) does not
	for hi-lo > 1 {
		mid := (lo + hi) / 2
		if len(build(withNote(mid))) <= MaxIssueURL {
			lo = mid
		} else {
			hi = mid
		}
	}
	link.URL, link.Truncated = build(withNote(lo)), true
	return link, nil
}

// oneLine collapses whitespace runs, including line breaks, and drops control characters.
func oneLine(s string) string {
	return strings.Join(strings.FieldsFunc(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && !unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s), unicode.IsSpace), " ")
}

// nameOK checks a label, milestone or type name.
func nameOK(s string) error {
	if utf8.RuneCountInString(s) > maxIssueName {
		return fmt.Errorf("is longer than %d characters", maxIssueName)
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return errors.New("must not contain control characters")
		}
	}
	return nil
}

// cleanList trims, drops empty and repeated entries, and checks each with ok.
func cleanList(what string, in []string, limit int, ok func(string) error) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		if err := ok(s); err != nil {
			return nil, fmt.Errorf("%s: %q %w", what, s, err)
		}
		seen[s] = true
		out = append(out, s)
	}
	if len(out) > limit {
		return nil, fmt.Errorf("%s: at most %d", what, limit)
	}
	return out, nil
}

// IssueEnv is what the diagnostics block says about this installation. Every field is a version or a platform name,
// never a setting, path, environment value or credential.
type IssueEnv struct {
	// Version is hal9001's version, Commit and BuildTime come from the build.
	Version, Commit, BuildTime string
	// OS and Arch are runtime.GOOS and runtime.GOARCH; MacOS is the macOS product version (empty elsewhere).
	OS, Arch, MacOS string
	GoVersion       string
}

// issueRefKeys are the ids a report may name, with their label in the diagnostics block. Values must be id-shaped.
var issueRefKeys = []struct{ key, label string }{
	{"flow_id", "Flow"},
	{"flow_run_id", "Flow run"},
	{"node_run_id", "Node run"},
	{"agent_run_id", "Agent run"},
	{"sensor_id", "Sensor"},
	{"event_id", "Event"},
}

// DiagnosticsBlock renders env and refs as a collapsed markdown section to append to an issue body.
func DiagnosticsBlock(env IssueEnv, refs map[string]string) string {
	var b strings.Builder
	b.WriteString("<details>\n<summary>Diagnostics</summary>\n\n")
	if env.Version != "" {
		b.WriteString("- hal9001: " + oneLine(env.Version))
		var build []string
		if env.Commit != "" {
			build = append(build, "commit "+oneLine(env.Commit))
		}
		if env.BuildTime != "" {
			build = append(build, "built "+oneLine(env.BuildTime))
		}
		if len(build) > 0 {
			b.WriteString(" (" + strings.Join(build, ", ") + ")")
		}
		b.WriteString("\n")
	}
	switch {
	case env.MacOS != "":
		b.WriteString("- macOS: " + oneLine(env.MacOS))
		if env.Arch != "" {
			b.WriteString(" (" + oneLine(env.Arch) + ")")
		}
		b.WriteString("\n")
	case env.OS != "":
		b.WriteString("- OS: " + oneLine(env.OS))
		if env.Arch != "" {
			b.WriteString("/" + oneLine(env.Arch))
		}
		b.WriteString("\n")
	}
	if env.GoVersion != "" {
		b.WriteString("- Go: " + oneLine(env.GoVersion) + "\n")
	}
	for _, k := range issueRefKeys {
		if v := refs[k.key]; v != "" {
			b.WriteString("- " + k.label + ": `" + v + "`\n")
		}
	}
	b.WriteString("\n</details>\n")
	return b.String()
}

// IssueLinkOptions configures IssueLinkTool.
type IssueLinkOptions struct {
	// Env returns the installation's version details for the diagnostics block. Nil means the block has no env lines.
	Env func() IssueEnv
	// Redact masks secret values in the title and body (the assist session's redactor). Nil leaves them alone.
	Redact func(string) string
}

// IssueLinkTool is the github_issue_link tool. It needs no approval: it only builds text.
func IssueLinkTool(opt IssueLinkOptions) Tool {
	refProps := map[string]any{}
	for _, k := range issueRefKeys {
		refProps[k.key] = prop("string", "The "+strings.ToLower(k.label)+" id.")
	}
	refsSchema := obj(refProps)
	refsSchema["description"] = "Ids of the flow, runs, sensor or event the report is about; shown in the Diagnostics block when diagnostics is true."
	schema := obj(map[string]any{
		"title":       prop("string", fmt.Sprintf("The issue title, one line (at most %d characters).", maxIssueTitle)),
		"body":        prop("string", fmt.Sprintf("The issue body as GitHub markdown (at most %d bytes): what happened, steps to reproduce, expected and actual behavior, relevant run ids. Never include secrets, tokens or environment values.", MaxIssueBody)),
		"labels":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Labels to apply (only if the repository has them and the user may add them)."},
		"assignees":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "GitHub usernames to assign."},
		"milestone":   prop("string", "Milestone name."),
		"template":    prop("string", "Issue template file name in the repository, for example bug_report.md."),
		"type":        prop("string", "Issue type, for example Bug."),
		"repo":        prop("string", "owner/name of the repository (default "+DefaultIssueRepo+": bugs and requests about hal9001 itself)."),
		"diagnostics": prop("boolean", "Append a collapsed Diagnostics block to the body: hal9001 version and build, macOS version and architecture, and the ids in refs. No settings, paths or secrets."),
		"refs":        refsSchema,
	}, "title")
	return Tool{
		Name: "github_issue_link",
		Description: "Build a link that opens GitHub's new-issue page with the title, body, labels and so on filled in (GitHub's URL query parameters). " +
			"Nothing is created and GitHub is not called: the user opens the link, reviews the form and submits it. No approval is needed. " +
			"Use it when the user reports a bug or asks you to file an issue: draft the issue, call this, and tell the user the link is in the chat. " +
			"The result is JSON {url, repo, title, body, truncated}; the chat shows an Open issue on GitHub button and Copy body. " +
			"If truncated is true the link carries only the start of the body: say so.",
		InputSchema: schema,
		Run: func(_ context.Context, c Call) (Result, error) {
			var a struct {
				Title       string            `json:"title"`
				Body        string            `json:"body"`
				Labels      []string          `json:"labels"`
				Assignees   []string          `json:"assignees"`
				Milestone   string            `json:"milestone"`
				Template    string            `json:"template"`
				Type        string            `json:"type"`
				Repo        string            `json:"repo"`
				Diagnostics bool              `json:"diagnostics"`
				Refs        map[string]string `json:"refs"`
			}
			dec := json.NewDecoder(bytes.NewReader(c.Args))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&a); err != nil {
				return Result{}, fmt.Errorf("arguments: %w", err)
			}
			if opt.Redact != nil {
				a.Title, a.Body = opt.Redact(a.Title), opt.Redact(a.Body)
			}
			if a.Diagnostics {
				refs, err := cleanRefs(a.Refs)
				if err != nil {
					return Result{}, err
				}
				var env IssueEnv
				if opt.Env != nil {
					env = opt.Env()
				}
				if strings.TrimSpace(a.Body) != "" {
					a.Body = strings.TrimRight(a.Body, "\n") + "\n\n"
				}
				a.Body += DiagnosticsBlock(env, refs)
			}
			link, err := BuildIssueLink(IssueLinkParams{
				Repo: a.Repo, Title: a.Title, Body: a.Body, Labels: a.Labels, Assignees: a.Assignees,
				Milestone: a.Milestone, Template: a.Template, Type: a.Type,
			})
			if err != nil {
				return Result{}, err
			}
			var out bytes.Buffer
			enc := json.NewEncoder(&out)
			enc.SetEscapeHTML(false)
			if err := enc.Encode(link); err != nil {
				return Result{}, err
			}
			return Result{Text: strings.TrimRight(out.String(), "\n")}, nil
		},
	}
}

// cleanRefs keeps the known keys; each value must be an id.
func cleanRefs(in map[string]string) (map[string]string, error) {
	known := map[string]bool{}
	for _, k := range issueRefKeys {
		known[k.key] = true
	}
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := map[string]string{}
	for _, k := range keys {
		v := strings.TrimSpace(in[k])
		switch {
		case !known[k]:
			return nil, fmt.Errorf("refs: unknown key %q", k)
		case v == "":
		case !idRE.MatchString(v):
			return nil, fmt.Errorf("refs.%s: %q is not an id", k, v)
		default:
			out[k] = v
		}
	}
	return out, nil
}
