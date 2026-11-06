package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// GHOptions configures the gh tools.
type GHOptions struct {
	// Bin is the gh executable (default "gh", looked up on Path).
	Bin string
	// Path is the PATH gh runs with; empty means the daemon's own.
	Path string
	// Timeout bounds one gh invocation (default 2m).
	Timeout time.Duration
	// MaxOutput truncates gh's output to this many bytes (default 256 KiB).
	MaxOutput int
}

const (
	defaultGHTimeout   = 2 * time.Minute
	defaultGHMaxOutput = 256 << 10
)

var (
	repoRE   = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	ghPathRE = regexp.MustCompile(`^/?[A-Za-z0-9][A-Za-z0-9._~\-/%@+,]*$`)
)

func (o GHOptions) bin() string {
	if o.Bin != "" {
		return o.Bin
	}
	return "gh"
}

func (o GHOptions) timeout() time.Duration {
	if o.Timeout > 0 {
		return o.Timeout
	}
	return defaultGHTimeout
}

func (o GHOptions) maxOutput() int {
	if o.MaxOutput > 0 {
		return o.MaxOutput
	}
	return defaultGHMaxOutput
}

// gh runs the gh CLI and returns its stdout (truncated with a note).
func (o GHOptions) gh(ctx context.Context, stdin []byte, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, o.timeout())
	defer cancel()
	bin := o.bin()
	env := os.Environ()
	if o.Path != "" {
		env = append(env, "PATH="+o.Path)
		if !strings.Contains(bin, "/") {
			// exec resolves a bare name against the daemon's PATH; resolve it against the configured one.
			if p, ok := lookPath(bin, o.Path); ok {
				bin = p
			}
		}
	}
	cmd := exec.CommandContext(ctx, bin, args...) //nolint:gosec // fixed binary, arguments are validated by the callers
	env = append(env, "GH_PROMPT_DISABLED=1", "NO_COLOR=1", "GH_NO_UPDATE_NOTIFIER=1", "GH_PAGER=cat")
	cmd.Env = env
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	text := out.String()
	if len(text) > o.maxOutput() {
		text = text[:o.maxOutput()] + fmt.Sprintf("\n[output truncated at %d bytes]", o.maxOutput())
	}
	if err != nil {
		if ctx.Err() != nil {
			return text, fmt.Errorf("gh timed out after %s", o.timeout())
		}
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		if text != "" {
			msg += "\n" + text
		}
		return "", errors.New("gh failed: " + msg)
	}
	return text, nil
}

func lookPath(name, pathEnv string) (string, bool) {
	for _, dir := range strings.Split(pathEnv, string(os.PathListSeparator)) {
		if dir == "" {
			continue
		}
		p := dir + string(os.PathSeparator) + name
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p, true
		}
	}
	return "", false
}

func ghTools(o GHOptions) []Tool {
	return []Tool{
		{
			Name: "gh_api",
			Description: "Call the GitHub REST API with the user's gh authentication. GET runs immediately; POST, PUT, PATCH and DELETE need the user's approval. " +
				"path is like /repos/OWNER/REPO/pulls/1. params are query parameters for GET and the JSON body otherwise.",
			InputSchema: obj(map[string]any{
				"method":   map[string]any{"type": "string", "enum": []string{"GET", "POST", "PUT", "PATCH", "DELETE"}, "default": "GET"},
				"path":     map[string]any{"type": "string", "description": "API path, e.g. /repos/OWNER/REPO/issues"},
				"params":   map[string]any{"type": "object", "description": "Query parameters (GET) or JSON body (other methods)"},
				"paginate": map[string]any{"type": "boolean", "description": "GET only: fetch every page"},
			}, "path"),
			Approval: ghAPIApproval,
			Run:      o.runGHAPI,
		},
		{
			Name:        "pr_view",
			Description: "View a pull request (title, body, state, author, reviews, checks, files) as JSON.",
			InputSchema: obj(map[string]any{"repo": repoProp, "number": numProp}, "repo", "number"),
			Run:         o.runPRView,
		},
		{
			Name:        "pr_diff",
			Description: "The unified diff of a pull request.",
			InputSchema: obj(map[string]any{"repo": repoProp, "number": numProp}, "repo", "number"),
			Run:         o.runPRDiff,
		},
		{
			Name:        "pr_list",
			Description: "List pull requests of a repository as JSON.",
			InputSchema: obj(map[string]any{
				"repo":   repoProp,
				"state":  map[string]any{"type": "string", "enum": []string{"open", "closed", "merged", "all"}, "default": "open"},
				"author": map[string]any{"type": "string"},
				"search": map[string]any{"type": "string", "description": "GitHub search qualifiers"},
				"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "default": 30},
			}, "repo"),
			Run: o.runPRList,
		},
		{
			Name:        "issue_view",
			Description: "View an issue (title, body, state, author, labels, comments) as JSON.",
			InputSchema: obj(map[string]any{"repo": repoProp, "number": numProp}, "repo", "number"),
			Run:         o.runIssueView,
		},
		{
			Name:        "search",
			Description: "Search GitHub: kind is issues, prs, repos, code or commits; query uses GitHub search syntax. Results are JSON.",
			InputSchema: obj(map[string]any{
				"kind":  map[string]any{"type": "string", "enum": []string{"issues", "prs", "repos", "code", "commits"}},
				"query": map[string]any{"type": "string"},
				"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "default": 30},
			}, "kind", "query"),
			Run: o.runSearch,
		},
	}
}

var (
	repoProp = map[string]any{"type": "string", "description": "OWNER/REPO"}
	numProp  = map[string]any{"type": "integer", "minimum": 1}
)

type ghAPIArgs struct {
	Method   string         `json:"method"`
	Path     string         `json:"path"`
	Params   map[string]any `json:"params"`
	Paginate bool           `json:"paginate"`
}

func parseGHAPI(args json.RawMessage) (ghAPIArgs, error) {
	var a ghAPIArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return a, fmt.Errorf("arguments: %w", err)
	}
	a.Method = strings.ToUpper(strings.TrimSpace(a.Method))
	if a.Method == "" {
		a.Method = "GET"
	}
	switch a.Method {
	case "GET", "POST", "PUT", "PATCH", "DELETE":
	default:
		return a, fmt.Errorf("method %q is not allowed", a.Method)
	}
	if !ghPathRE.MatchString(strings.SplitN(a.Path, "?", 2)[0]) || strings.Contains(a.Path, "..") {
		return a, fmt.Errorf("path %q is not a GitHub API path", a.Path)
	}
	return a, nil
}

// ghAPIApproval: everything but GET needs approval. Unparsable arguments are left to Run to reject.
func ghAPIApproval(args json.RawMessage) (bool, string) {
	a, err := parseGHAPI(args)
	if err != nil {
		if m := writeMethod(args); m != "" {
			return true, "gh_api " + m + " (arguments invalid)"
		}
		return false, ""
	}
	if a.Method == "GET" {
		return false, ""
	}
	return true, fmt.Sprintf("gh_api %s %s changes GitHub", a.Method, a.Path)
}

func writeMethod(args json.RawMessage) string {
	var a struct {
		Method string `json:"method"`
	}
	if json.Unmarshal(args, &a) == nil {
		if m := strings.ToUpper(strings.TrimSpace(a.Method)); m != "" && m != "GET" {
			return m
		}
	}
	return ""
}

func (o GHOptions) runGHAPI(ctx context.Context, c Call) (Result, error) {
	a, err := parseGHAPI(c.Args)
	if err != nil {
		return Result{}, err
	}
	argv := []string{"api", "-X", a.Method}
	var stdin []byte
	if a.Method == "GET" {
		if a.Paginate {
			argv = append(argv, "--paginate")
		}
		keys := make([]string, 0, len(a.Params))
		for k := range a.Params {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			argv = append(argv, "-f", k+"="+paramString(a.Params[k]))
		}
	} else if len(a.Params) > 0 {
		if stdin, err = json.Marshal(a.Params); err != nil {
			return Result{}, err
		}
		argv = append(argv, "--input", "-")
	}
	argv = append(argv, "--", a.Path)
	out, err := o.gh(ctx, stdin, argv...)
	return Result{Text: out}, err
}

func paramString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return ""
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}

type repoNumArgs struct {
	Repo   string `json:"repo"`
	Number int    `json:"number"`
}

func parseRepoNum(args json.RawMessage) (repoNumArgs, error) {
	var a repoNumArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return a, fmt.Errorf("arguments: %w", err)
	}
	if !repoRE.MatchString(a.Repo) {
		return a, fmt.Errorf("repo %q must be OWNER/REPO", a.Repo)
	}
	if a.Number <= 0 {
		return a, errors.New("number must be a positive integer")
	}
	return a, nil
}

const (
	prFields    = "number,title,body,state,isDraft,author,url,baseRefName,headRefName,labels,reviewDecision,reviews,statusCheckRollup,files,additions,deletions,createdAt,updatedAt,mergeable"
	issueFields = "number,title,body,state,author,url,labels,assignees,comments,createdAt,updatedAt"
)

func (o GHOptions) runPRView(ctx context.Context, c Call) (Result, error) {
	a, err := parseRepoNum(c.Args)
	if err != nil {
		return Result{}, err
	}
	out, err := o.gh(ctx, nil, "pr", "view", strconv.Itoa(a.Number), "-R", a.Repo, "--json", prFields)
	return Result{Text: out}, err
}

func (o GHOptions) runPRDiff(ctx context.Context, c Call) (Result, error) {
	a, err := parseRepoNum(c.Args)
	if err != nil {
		return Result{}, err
	}
	out, err := o.gh(ctx, nil, "pr", "diff", strconv.Itoa(a.Number), "-R", a.Repo)
	return Result{Text: out}, err
}

func (o GHOptions) runIssueView(ctx context.Context, c Call) (Result, error) {
	a, err := parseRepoNum(c.Args)
	if err != nil {
		return Result{}, err
	}
	out, err := o.gh(ctx, nil, "issue", "view", strconv.Itoa(a.Number), "-R", a.Repo, "--json", issueFields)
	return Result{Text: out}, err
}

func clampLimit(n int) string {
	if n <= 0 {
		n = 30
	}
	if n > 100 {
		n = 100
	}
	return strconv.Itoa(n)
}

func (o GHOptions) runPRList(ctx context.Context, c Call) (Result, error) {
	var a struct {
		Repo   string `json:"repo"`
		State  string `json:"state"`
		Author string `json:"author"`
		Search string `json:"search"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal(c.Args, &a); err != nil {
		return Result{}, fmt.Errorf("arguments: %w", err)
	}
	if !repoRE.MatchString(a.Repo) {
		return Result{}, fmt.Errorf("repo %q must be OWNER/REPO", a.Repo)
	}
	argv := []string{"pr", "list", "-R", a.Repo, "--limit", clampLimit(a.Limit), "--json", "number,title,state,isDraft,author,url,headRefName,labels,reviewDecision,createdAt,updatedAt"}
	switch a.State {
	case "", "open":
	case "closed", "merged", "all":
		argv = append(argv, "--state", a.State)
	default:
		return Result{}, fmt.Errorf("state %q is not one of open, closed, merged, all", a.State)
	}
	if a.Author != "" {
		if strings.HasPrefix(a.Author, "-") {
			return Result{}, errors.New("invalid author")
		}
		argv = append(argv, "--author", a.Author)
	}
	if a.Search != "" {
		if strings.HasPrefix(a.Search, "-") {
			return Result{}, errors.New("search must not start with '-'")
		}
		argv = append(argv, "--search", a.Search)
	}
	out, err := o.gh(ctx, nil, argv...)
	return Result{Text: out}, err
}

var searchFields = map[string]string{
	"issues":  "number,title,state,url,repository,author,labels,updatedAt",
	"prs":     "number,title,state,url,repository,author,labels,updatedAt",
	"repos":   "fullName,description,url,stargazersCount,updatedAt",
	"code":    "path,repository,url",
	"commits": "sha,commit,repository,url",
}

func (o GHOptions) runSearch(ctx context.Context, c Call) (Result, error) {
	var a struct {
		Kind  string `json:"kind"`
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(c.Args, &a); err != nil {
		return Result{}, fmt.Errorf("arguments: %w", err)
	}
	fields, ok := searchFields[a.Kind]
	if !ok {
		return Result{}, fmt.Errorf("kind %q is not one of issues, prs, repos, code, commits", a.Kind)
	}
	if strings.TrimSpace(a.Query) == "" {
		return Result{}, errors.New("query is required")
	}
	out, err := o.gh(ctx, nil, "search", a.Kind, "--limit", clampLimit(a.Limit), "--json", fields, "--", a.Query)
	return Result{Text: out}, err
}
