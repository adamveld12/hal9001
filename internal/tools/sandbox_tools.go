package tools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"hal9001/internal/sandbox"
)

// SandboxOptions configures SandboxProvider.
type SandboxOptions struct {
	// GitToken returns the token used to clone RunSpec.Repo; nil uses the user's `gh auth token`.
	GitToken func(ctx context.Context) string
}

// maxReadBytes caps what fs_read returns in one call.
const maxReadBytes = 256 << 10

// SandboxProvider adds fs_read, fs_write, fs_list, fs_search and shell to runs whose node names a sandbox. The
// session is opened through m with the node's image, repo and ref and the node input as input.json; it closes
// with the run (a failed run keeps its workspace). fs_write and shell approvals follow the sandbox's policy.
func SandboxProvider(m *sandbox.Manager, opt SandboxOptions) Provider {
	return func(ctx context.Context, spec RunSpec) (Extension, error) {
		if spec.Sandbox == "" {
			return Extension{}, nil
		}
		policy, err := m.Policy(spec.Sandbox)
		if err != nil {
			return Extension{}, err
		}
		info := spec.Info()
		// The workspace is keyed by the node run, so the propose and execute phases of an approve node share it.
		key := spec.RunID
		if info.NodeRunID != "" {
			key = info.NodeRunID
		}
		rs := sandbox.RunSpec{RunID: key, Image: spec.Image, Repo: spec.Repo, Ref: spec.Ref}
		if len(info.Input) > 0 {
			rs.Input = info.Input
		}
		if spec.Repo != "" {
			if opt.GitToken != nil {
				rs.GitToken = opt.GitToken(ctx)
			} else {
				rs.GitToken = sandbox.GitHubToken(ctx)
			}
		}
		sess, err := m.Open(ctx, spec.Sandbox, rs)
		if err != nil {
			return Extension{}, fmt.Errorf("sandbox %s: %w", spec.Sandbox, err)
		}
		st := &sandboxTools{name: spec.Sandbox, sess: sess, policy: policy}
		return Extension{
			Tools:     st.tools(),
			Sandbox:   sess,
			CloseWith: func(success bool) { _ = sess.Close(context.WithoutCancel(ctx), success) },
		}, nil
	}
}

type sandboxTools struct {
	name   string
	sess   sandbox.Session
	policy *sandbox.Policy
}

func objSchema(required []string, props map[string]any) map[string]any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }

func (s *sandboxTools) tools() []Tool {
	where := fmt.Sprintf("in the %q sandbox workdir %s", s.name, s.sess.Workdir())
	return []Tool{
		{
			Name: "fs_read",
			Description: "Read a text file " + where + ". Paths are relative to the workdir; paths outside it are rejected. " +
				fmt.Sprintf("Returns at most %d bytes; use offset to read further.", maxReadBytes),
			InputSchema: objSchema([]string{"path"}, map[string]any{
				"path":   str("File path relative to the workdir."),
				"offset": map[string]any{"type": "integer", "minimum": 0, "description": "Byte offset to start at (default 0)."},
			}),
			Run: s.read,
		},
		{
			Name: "fs_write",
			Description: "Create or replace a file " + where + " with the given content, creating parent directories. " +
				"Paths outside the workdir are rejected. May need the user's approval, which shows a diff.",
			InputSchema: objSchema([]string{"path", "content"}, map[string]any{
				"path":    str("File path relative to the workdir."),
				"content": str("The complete new content of the file."),
			}),
			Policy: s.writePolicy,
			Run:    s.write,
		},
		{
			Name:        "fs_list",
			Description: "List a directory " + where + ". Directories are marked with a trailing slash.",
			InputSchema: objSchema(nil, map[string]any{
				"path": str("Directory relative to the workdir (default: the workdir)."),
			}),
			Run: s.list,
		},
		{
			Name: "fs_search",
			Description: "Search file contents " + where + " for a regular expression (extended syntax). Returns " +
				fmt.Sprintf("path:line:text for at most %d matches; skips binary files and .git.", sandbox.MaxMatches),
			InputSchema: objSchema([]string{"pattern"}, map[string]any{
				"pattern": str("Regular expression."),
				"path":    str("File or directory to search, relative to the workdir (default: the workdir)."),
			}),
			Run: s.search,
		},
		{
			Name: "shell",
			Description: "Run a shell command (sh -c) " + where + "; the working directory is the workdir. Returns stdout, stderr and " +
				"the exit code; a non-zero exit is not a tool error. May need the user's approval.",
			InputSchema: objSchema([]string{"command"}, map[string]any{
				"command":         str("The command line."),
				"stdin":           str("Optional text sent to the command's standard input."),
				"timeout_seconds": map[string]any{"type": "integer", "minimum": 1, "description": "Kill the command after this many seconds (default: the sandbox's command timeout)."},
			}),
			Policy: s.shellPolicy,
			Run:    s.shell,
		},
	}
}

func decode(raw json.RawMessage, dst any) error {
	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}
	return nil
}

func (s *sandboxTools) read(ctx context.Context, c Call) (Result, error) {
	var a struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
	}
	if err := decode(c.Args, &a); err != nil {
		return Result{}, err
	}
	data, err := s.sess.ReadFile(ctx, a.Path)
	if err != nil {
		return Result{}, err
	}
	if a.Offset > len(data) {
		return Result{}, fmt.Errorf("offset %d is past the end of the file (%d bytes)", a.Offset, len(data))
	}
	data = data[a.Offset:]
	if len(data) > maxReadBytes {
		next := a.Offset + maxReadBytes
		return Result{Text: string(data[:maxReadBytes]) + fmt.Sprintf("\n[truncated: %d more bytes; call again with offset %d]", len(data)-maxReadBytes, next)}, nil
	}
	return Result{Text: string(data)}, nil
}

// diffSnippetLimit bounds the diff shown in an approval.
const diffSnippetLimit = 16 << 10

func (s *sandboxTools) writePolicy(raw json.RawMessage) (Verdict, string) {
	var a struct{ Path, Content string }
	if json.Unmarshal(raw, &a) != nil {
		return VerdictAllow, ""
	}
	d := s.policy.Decide(sandbox.OpFSWrite, map[string]any{"path": a.Path})
	switch d.Action {
	case "deny":
		return VerdictDeny, fmt.Sprintf("fs_write to %s is denied by the %s sandbox policy: %s", a.Path, s.name, d.Reason)
	case "approve":
	default:
		return VerdictAllow, ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	old, err := s.sess.ReadFile(ctx, a.Path)
	label := "modify " + a.Path
	switch {
	case errors.Is(err, sandbox.ErrNotFound):
		old, label = nil, "create "+a.Path
	case err != nil:
		old, label = nil, "write "+a.Path+" (current content unreadable: "+err.Error()+")"
	}
	diff := UnifiedDiff(a.Path, string(old), a.Content)
	if len(diff) > diffSnippetLimit {
		diff = diff[:diffSnippetLimit] + "\n[diff truncated]"
	}
	return VerdictApprove, fmt.Sprintf("sandbox %s: %s (%s)\n\n%s", s.name, label, d.Reason, diff)
}

func (s *sandboxTools) write(ctx context.Context, c Call) (Result, error) {
	var a struct{ Path, Content string }
	if err := decode(c.Args, &a); err != nil {
		return Result{}, err
	}
	if err := s.sess.WriteFile(ctx, a.Path, []byte(a.Content)); err != nil {
		return Result{}, err
	}
	return Result{Text: fmt.Sprintf("wrote %d bytes to %s", len(a.Content), a.Path)}, nil
}

func (s *sandboxTools) list(ctx context.Context, c Call) (Result, error) {
	var a struct{ Path string }
	if err := decode(c.Args, &a); err != nil {
		return Result{}, err
	}
	ents, err := s.sess.List(ctx, a.Path)
	if err != nil {
		return Result{}, err
	}
	var b strings.Builder
	for _, e := range ents {
		if e.IsDir {
			fmt.Fprintf(&b, "%s/\n", e.Name)
		} else {
			fmt.Fprintf(&b, "%s\t%d bytes\n", e.Name, e.Size)
		}
	}
	if b.Len() == 0 {
		return Result{Text: "(empty directory)"}, nil
	}
	return Result{Text: b.String()}, nil
}

func (s *sandboxTools) search(ctx context.Context, c Call) (Result, error) {
	var a struct{ Pattern, Path string }
	if err := decode(c.Args, &a); err != nil {
		return Result{}, err
	}
	hits, err := s.sess.Search(ctx, a.Path, a.Pattern)
	if err != nil {
		return Result{}, err
	}
	if len(hits) == 0 {
		return Result{Text: "no matches"}, nil
	}
	var b strings.Builder
	for _, h := range hits {
		text := h.Text
		if len(text) > 300 {
			text = text[:300] + "…"
		}
		fmt.Fprintf(&b, "%s:%d:%s\n", h.Path, h.Line, text)
	}
	return Result{Text: b.String()}, nil
}

func (s *sandboxTools) shellPolicy(raw json.RawMessage) (Verdict, string) {
	var a struct{ Command string }
	if json.Unmarshal(raw, &a) != nil {
		return VerdictAllow, ""
	}
	d := s.policy.Decide(sandbox.OpShell, map[string]any{"command": a.Command})
	switch d.Action {
	case "deny":
		return VerdictDeny, fmt.Sprintf("shell command is denied by the %s sandbox policy: %s", s.name, d.Reason)
	case "approve":
	default:
		return VerdictAllow, ""
	}
	return VerdictApprove, fmt.Sprintf("sandbox %s: run a shell command (%s):\n\n%s", s.name, d.Reason, a.Command)
}

func (s *sandboxTools) shell(ctx context.Context, c Call) (Result, error) {
	var a struct {
		Command        string `json:"command"`
		Stdin          string `json:"stdin"`
		TimeoutSeconds int    `json:"timeout_seconds"`
	}
	if err := decode(c.Args, &a); err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(a.Command) == "" {
		return Result{}, errors.New("command is empty")
	}
	var stdin []byte
	if a.Stdin != "" {
		stdin = []byte(a.Stdin)
	}
	res, err := s.sess.Exec(ctx, a.Command, stdin, time.Duration(a.TimeoutSeconds)*time.Second)
	if err != nil {
		return Result{}, err
	}
	return Result{Text: FormatExec(res)}, nil
}

// FormatExec renders a command result for the agent.
func FormatExec(res sandbox.ExecResult) string {
	var b strings.Builder
	switch {
	case res.TimedOut:
		b.WriteString("exit: timed out\n")
	default:
		fmt.Fprintf(&b, "exit: %d\n", res.ExitCode)
	}
	if res.Stdout != "" {
		b.WriteString("stdout:\n" + res.Stdout)
		if !strings.HasSuffix(res.Stdout, "\n") {
			b.WriteByte('\n')
		}
	}
	if res.Stderr != "" {
		b.WriteString("stderr:\n" + res.Stderr)
		if !strings.HasSuffix(res.Stderr, "\n") {
			b.WriteByte('\n')
		}
	}
	if res.Truncated {
		b.WriteString("[output truncated]\n")
	}
	return b.String()
}

// RunInSandbox runs a tool script inside an open sandbox session and returns the result.
//
// lang is bash, python, node or exec. For the first three, script is the source: it is written to a temporary
// file under .hal/ in the workdir, run with bash, python3 or node, and removed afterwards. For exec, script is a
// command line run directly with sh -c. stdin is sent to the process. env is exported for this one command; its
// values are treated as secrets and replaced by sandbox.Redacted in the result. A zero timeout means the
// sandbox's command timeout.
func RunInSandbox(ctx context.Context, sess sandbox.Session, lang, script string, stdin []byte, env map[string]string, timeout time.Duration) (sandbox.ExecResult, error) {
	return RunInSandboxEnv(ctx, sess, lang, script, stdin, env, nil, timeout)
}

// RunInSandboxEnv is RunInSandbox with plain as well: variables exported like env but not treated as secrets (a
// tool's ARG_ variables, whose values the result may legitimately contain).
func RunInSandboxEnv(ctx context.Context, sess sandbox.Session, lang, script string, stdin []byte, env, plain map[string]string, timeout time.Duration) (sandbox.ExecResult, error) {
	var runner, ext string
	switch lang {
	case "bash":
		runner, ext = "bash", "sh"
	case "python":
		runner, ext = "python3", "py"
	case "node":
		runner, ext = "node", "js"
	case "exec":
	default:
		return sandbox.ExecResult{}, fmt.Errorf("unknown tool language %q", lang)
	}
	var b strings.Builder
	for _, vars := range []map[string]string{plain, env} {
		for k, v := range vars {
			if !validEnvName(k) {
				return sandbox.ExecResult{}, fmt.Errorf("invalid environment variable name %q", k)
			}
			fmt.Fprintf(&b, "export %s=%s\n", k, shellQuote(v))
		}
	}
	cmd := script
	if runner != "" {
		var raw [6]byte
		_, _ = rand.Read(raw[:])
		file := ".hal/tool-" + hex.EncodeToString(raw[:]) + "." + ext
		if err := sess.WriteFile(ctx, file, []byte(script)); err != nil {
			return sandbox.ExecResult{}, err
		}
		cmd = runner + " " + file + "; code=$?; rm -f " + file + "; exit $code"
	}
	res, err := sess.Exec(ctx, b.String()+cmd, stdin, timeout)
	if err != nil {
		return res, err
	}
	for _, v := range env {
		if len(v) >= 4 {
			res.Stdout = strings.ReplaceAll(res.Stdout, v, sandbox.Redacted)
			res.Stderr = strings.ReplaceAll(res.Stderr, v, sandbox.Redacted)
		}
	}
	return res, nil
}

func validEnvName(k string) bool {
	if k == "" {
		return false
	}
	for i, r := range k {
		if r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// UnifiedDiff renders a unified diff (3 lines of context) of old to new for path. Identical texts give "(no changes)".
func UnifiedDiff(path, old, new string) string {
	a, b := splitLines(old), splitLines(new)
	if old == new {
		return "(no changes)"
	}
	// LCS table over lines; inputs are bounded by the caller's approval snippet, but cap the table anyway.
	if len(a)*len(b) > 4_000_000 {
		return fmt.Sprintf("--- a/%s\n+++ b/%s\n(file too large to diff: %d lines -> %d lines)", path, path, len(a), len(b))
	}
	lcs := make([][]int, len(a)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	type op struct {
		kind byte // ' ', '-', '+'
		text string
	}
	var ops []op
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		switch {
		case i < len(a) && j < len(b) && a[i] == b[j]:
			ops = append(ops, op{' ', a[i]})
			i++
			j++
		case j < len(b) && (i == len(a) || lcs[i][j+1] >= lcs[i+1][j]):
			ops = append(ops, op{'+', b[j]})
			j++
		default:
			ops = append(ops, op{'-', a[i]})
			i++
		}
	}
	const ctxLines = 3
	keep := make([]bool, len(ops))
	for k, o := range ops {
		if o.kind == ' ' {
			continue
		}
		for d := -ctxLines; d <= ctxLines; d++ {
			if k+d >= 0 && k+d < len(ops) {
				keep[k+d] = true
			}
		}
	}
	var out strings.Builder
	fmt.Fprintf(&out, "--- a/%s\n+++ b/%s\n", path, path)
	oldLine, newLine := 1, 1
	for k := 0; k < len(ops); {
		if !keep[k] {
			if ops[k].kind != '+' {
				oldLine++
			}
			if ops[k].kind != '-' {
				newLine++
			}
			k++
			continue
		}
		end := k
		for end < len(ops) && keep[end] {
			end++
		}
		oc, nc := 0, 0
		for _, o := range ops[k:end] {
			if o.kind != '+' {
				oc++
			}
			if o.kind != '-' {
				nc++
			}
		}
		fmt.Fprintf(&out, "@@ -%d,%d +%d,%d @@\n", oldLine, oc, newLine, nc)
		for _, o := range ops[k:end] {
			out.WriteByte(o.kind)
			out.WriteString(o.text)
			out.WriteByte('\n')
		}
		oldLine += oc
		newLine += nc
		k = end
	}
	return strings.TrimRight(out.String(), "\n")
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}
