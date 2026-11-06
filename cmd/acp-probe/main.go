// Command acp-probe is a diagnostic ACP (Agent Client Protocol) client. It
// spawns an agent command over stdio, performs the handshake, prints what the
// agent reports (info, capabilities, session modes, models, config options),
// runs scripted prompts in a scratch directory, logs every JSON-RPC message as
// JSONL, and answers session/request_permission by a policy flag.
//
// Usage:
//
//	acp-probe [flags] -- <agent command> [args...]
//	acp-probe mcp-echo [logfile [delay]]   (internal: stdio MCP server used by --mcp-echo)
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/coder/acp-go-sdk"
)

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

type options struct {
	agent       []string
	cwd         string
	policy      string
	allowAlways bool
	mode        string
	model       string
	configs     multiFlag
	raws        multiFlag
	prompts     multiFlag
	promptFile  string
	mcpEcho     bool
	mcpStdio    multiFlag // name=command arg...
	mcpDelay    time.Duration
	canary      bool
	env         multiFlag
	loadID      string
	resumeID    string
	resumeCheck bool
	list        bool
	elicitation bool
	sessionMeta string
	setupFiles  multiFlag
	noPrompt    bool
	logPath     string
	timeout     time.Duration
}

const (
	canaryAgents = "CANARY-AGENTS-MD-7421"
	canaryClaude = "CANARY-CLAUDE-MD-7421"
)

func pf(w io.Writer, format string, args ...any) { _, _ = fmt.Fprintf(w, format, args...) }

func pln(w io.Writer, args ...any) { _, _ = fmt.Fprintln(w, args...) }

func main() {
	if len(os.Args) > 1 && os.Args[1] == "mcp-echo" {
		logPath := ""
		if len(os.Args) > 2 {
			logPath = os.Args[2]
		}
		var delay time.Duration
		if len(os.Args) > 3 {
			d, err := time.ParseDuration(os.Args[3])
			if err != nil {
				pln(os.Stderr, "mcp-echo: bad delay:", err)
				os.Exit(2)
			}
			delay = d
		}
		if err := runMCPEcho(logPath, delay, os.Stdin, os.Stdout); err != nil {
			pln(os.Stderr, "mcp-echo:", err)
			os.Exit(1)
		}
		return
	}
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func parseFlags(args []string, stderr io.Writer) (*options, error) {
	o := &options{}
	fs := flag.NewFlagSet("acp-probe", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		pln(stderr, "usage: acp-probe [flags] -- <agent command> [args...]")
		fs.PrintDefaults()
	}
	fs.StringVar(&o.cwd, "cwd", "", "session working directory (default: fresh temp dir seeded with hello.txt)")
	fs.StringVar(&o.policy, "policy", policyLogAndReject, "permission policy: allow-all | reject-all | log-and-reject | cancel")
	fs.BoolVar(&o.allowAlways, "allow-always", false, "with allow-all, choose allow_always instead of allow_once")
	fs.StringVar(&o.mode, "mode", "", "session mode id to set after the session is created/loaded")
	fs.StringVar(&o.model, "model", "", "model id: set via the 'model' config option, or session/set_model if the agent reports models")
	fs.Var(&o.configs, "config", "session config option as id=value (repeatable)")
	fs.Var(&o.raws, "raw", "extra raw request after session setup: method=<json params>; {{sid}} expands to the session id (repeatable)")
	fs.Var(&o.prompts, "prompt", "prompt text to send, in order (repeatable; default: scripted read/write/exec[/mcp] prompt)")
	fs.StringVar(&o.promptFile, "prompt-file", "", "read one prompt from this file")
	fs.BoolVar(&o.mcpEcho, "mcp-echo", false, "pass a local stdio MCP server exposing an 'echo' tool in session/new")
	fs.DurationVar(&o.mcpDelay, "mcp-echo-delay", 0, "with --mcp-echo: the echo tool holds its reply back this long (tests tool-call timeouts)")
	fs.Var(&o.mcpStdio, "mcp-stdio", "pass an extra stdio MCP server in session/new as name=command arg... (repeatable)")
	fs.BoolVar(&o.canary, "canary", false, "write AGENTS.md and CLAUDE.md with canary tokens into the working directory")
	fs.Var(&o.env, "env", "KEY=VALUE added to the agent process environment (repeatable)")
	fs.StringVar(&o.loadID, "load", "", "call session/load with this session id instead of session/new")
	fs.StringVar(&o.resumeID, "resume", "", "call session/resume with this session id instead of session/new")
	fs.BoolVar(&o.resumeCheck, "resume-check", false, "scripted check: prompt a secret, restart the agent, session/load, ask for the secret")
	fs.StringVar(&o.sessionMeta, "session-meta", "", "JSON object sent as _meta in session/new|load|resume (e.g. Claude: {\"claudeCode\":{\"options\":{\"settingSources\":[]}}})")
	fs.Var(&o.setupFiles, "write-file", "relative/path=content written into the work dir before starting (repeatable; \\n expands to newline)")
	fs.BoolVar(&o.elicitation, "elicitation", false, "advertise elicitation.form so agents (omp) route generic tool approvals through elicitation/create")
	fs.BoolVar(&o.list, "list", false, "call session/list after initialize")
	fs.BoolVar(&o.noPrompt, "no-prompt", false, "stop after session setup; send no prompt")
	fs.StringVar(&o.logPath, "log", "", "JSONL wire log path (default: <tempdir>/wire.jsonl; '-' = stderr)")
	fs.DurationVar(&o.timeout, "timeout", 3*time.Minute, "per-request timeout for prompts")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	o.agent = fs.Args()
	if len(o.agent) == 0 {
		fs.Usage()
		return nil, errors.New("missing agent command after --")
	}
	if !validPolicy(o.policy) {
		return nil, fmt.Errorf("invalid --policy %q", o.policy)
	}
	if o.loadID != "" && o.resumeID != "" {
		return nil, errors.New("--load and --resume are mutually exclusive")
	}
	return o, nil
}

func run(args []string, stdout, stderr io.Writer) int {
	o, err := parseFlags(args, stderr)
	if err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			pln(stderr, "acp-probe:", err)
		}
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := execute(ctx, o, stdout); err != nil {
		pln(stderr, "acp-probe:", err)
		return 1
	}
	return 0
}

func execute(ctx context.Context, o *options, out io.Writer) error {
	root, err := os.MkdirTemp("", "acp-probe-")
	if err != nil {
		return err
	}
	work := o.cwd
	if work == "" {
		work = filepath.Join(root, "work")
		if err := os.Mkdir(work, 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(work, "hello.txt"), []byte("hello from the probe\n"), 0o600); err != nil {
			return err
		}
	}
	work, err = filepath.Abs(work)
	if err != nil {
		return err
	}
	if o.canary {
		if err := writeCanaries(work); err != nil {
			return err
		}
	}
	for _, wf := range o.setupFiles {
		rel, content, ok := strings.Cut(wf, "=")
		if !ok || filepath.IsAbs(rel) || strings.Contains(rel, "..") {
			return fmt.Errorf("--write-file %q: want relative/path=content", wf)
		}
		dst := filepath.Join(work, rel)
		//nolint:gosec // paths come from the operator's own command line; the relative-path check above keeps them inside the work dir
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
		//nolint:gosec // see above
		if err := os.WriteFile(dst, []byte(strings.ReplaceAll(content, `\n`, "\n")), 0o600); err != nil {
			return err
		}
	}
	var logOut io.Writer
	switch o.logPath {
	case "-":
		logOut = os.Stderr
	case "":
		f, err := os.Create(filepath.Join(root, "wire.jsonl")) //nolint:gosec // path built from our own temp dir
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		logOut = f
		o.logPath = f.Name()
	default:
		f, err := os.Create(o.logPath) //nolint:gosec // operator-chosen log path
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		logOut = f
	}
	mcpLog := filepath.Join(root, "mcp-calls.log")
	pf(out, "agent command : %s\nwork dir      : %s\nwire log      : %s\npolicy        : %s\n", strings.Join(o.agent, " "), work, o.logPath, o.policy)
	if o.mcpEcho {
		pf(out, "mcp call log  : %s\n", mcpLog)
	}

	prompts, err := scriptedPrompts(o)
	if err != nil {
		return err
	}
	pr := &prober{opts: o, out: out, work: work, mcpLog: mcpLog, logOut: logOut, prompts: prompts}
	defer pr.summary()
	if o.resumeCheck {
		return pr.resumeCheck(ctx)
	}
	return pr.single(ctx)
}

func writeCanaries(dir string) error {
	files := map[string]string{
		"AGENTS.md": "# Project instructions\n\nThe project canary token from AGENTS.md is " + canaryAgents + ".\n",
		"CLAUDE.md": "# Project instructions\n\nThe project canary token from CLAUDE.md is " + canaryClaude + ".\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil { //nolint:gosec // dir is the operator's own work dir
			return err
		}
	}
	return nil
}

func scriptedPrompts(o *options) ([]string, error) {
	prompts := append([]string(nil), o.prompts...)
	if o.promptFile != "" {
		b, err := os.ReadFile(o.promptFile) //nolint:gosec // operator-chosen path
		if err != nil {
			return nil, err
		}
		prompts = append(prompts, string(b))
	}
	if len(prompts) > 0 || o.noPrompt || o.resumeCheck {
		return prompts, nil
	}
	p := "Do these steps in the current directory, using your tools, and stop at the first step that is refused: " +
		"1) read the file hello.txt. " +
		"2) create a file named note.txt containing exactly the line: probe. " +
		"3) run the shell command: echo probe-ok. "
	if o.mcpEcho {
		p += "4) call the echo tool of the probe_echo MCP server with text mcp-ok. "
	}
	p += "Finish with one short sentence saying which steps succeeded and which were refused."
	return []string{p}, nil
}

// prober drives one scenario and remembers enough to print a final summary.
type prober struct {
	opts    *options
	out     io.Writer
	work    string
	mcpLog  string
	logOut  io.Writer
	prompts []string

	client *probeClient
	last   *proc // most recently started process, for the summary

	sessionResult json.RawMessage // raw result of the latest session/new|load|resume
}

// proc is one running agent process with its ACP connection.
type proc struct {
	cmd   *exec.Cmd
	stdin io.Closer
	conn  *acp.ClientSideConnection
	wire  *wireLog
	done  chan struct{}
}

func (p *prober) start(ctx context.Context) (*proc, error) {
	o := p.opts
	cmd := exec.CommandContext(ctx, o.agent[0], o.agent[1:]...) //nolint:gosec // running a user-chosen agent command is the purpose of this tool
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.Dir = p.work
	cmd.Env = append(os.Environ(), o.env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start agent: %w", err)
	}
	wire := newWireLog(p.logOut)
	if p.client == nil {
		p.client = newProbeClient(p.out, o.policy, o.allowAlways)
	}
	wire.onAgent = p.client.handleAgentMsg
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		buf := make([]byte, 64<<10)
		var acc bytes.Buffer
		for {
			n, err := stderr.Read(buf)
			acc.Write(buf[:n])
			for {
				i := bytes.IndexByte(acc.Bytes(), '\n')
				if i < 0 {
					break
				}
				line := string(acc.Next(i + 1))
				wire.stderrLine(line)
				p.client.printf("[agent stderr] %s", line)
			}
			if err != nil {
				return
			}
		}
	}()
	rd, readDone := wire.reader(stdout)
	conn := acp.NewClientSideConnection(p.client, wire.writer(stdin), rd)
	pr := &proc{cmd: cmd, stdin: stdin, conn: conn, wire: wire, done: make(chan struct{})}
	go func() {
		// cmd.Wait closes the pipes, so let both readers drain first.
		<-readDone
		<-stderrDone
		_ = cmd.Wait()
		close(pr.done)
	}()
	p.last = pr
	return pr, nil
}

// stop closes the agent's stdin, then escalates to SIGTERM and SIGKILL on the process group.
func (pr *proc) stop() {
	if pr == nil {
		return
	}
	_ = pr.stdin.Close()
	select {
	case <-pr.done:
		return
	case <-time.After(3 * time.Second):
	}
	_ = syscall.Kill(-pr.cmd.Process.Pid, syscall.SIGTERM)
	select {
	case <-pr.done:
		return
	case <-time.After(3 * time.Second):
	}
	_ = syscall.Kill(-pr.cmd.Process.Pid, syscall.SIGKILL)
	<-pr.done
}

func (p *prober) initialize(ctx context.Context, pr *proc) error {
	ictx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	t0 := time.Now()
	_, err := pr.conn.Initialize(ictx, acp.InitializeRequest{
		ProtocolVersion:    acp.ProtocolVersionNumber,
		ClientInfo:         &acp.Implementation{Name: "hal9001-acp-probe", Version: "0"},
		ClientCapabilities: p.clientCaps(),
	})
	if err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	pf(p.out, "\n--- initialize (%s)\n", time.Since(t0).Round(time.Millisecond))
	printInit(p.out, pr.wire.result("initialize"))
	return nil
}

func (p *prober) clientCaps() acp.ClientCapabilities {
	caps := acp.ClientCapabilities{}
	if p.opts.elicitation {
		caps.Elicitation = &acp.ElicitationCapabilities{Form: &acp.ElicitationFormCapabilities{}}
	}
	return caps
}

// meta parses --session-meta; it returns nil when unset.
func (p *prober) meta() (map[string]any, error) {
	if p.opts.sessionMeta == "" {
		return nil, nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(p.opts.sessionMeta), &m); err != nil {
		return nil, fmt.Errorf("--session-meta: %w", err)
	}
	return m, nil
}

func (p *prober) mcpServers() []acp.McpServer {
	servers := []acp.McpServer{}
	if p.opts.mcpEcho {
		self, err := os.Executable()
		if err != nil {
			self = os.Args[0]
		}
		servers = append(servers, acp.McpServer{Stdio: &acp.McpServerStdio{
			Name:    "probe_echo",
			Command: self,
			Args:    mcpEchoArgs(p.mcpLog, p.opts.mcpDelay),
			Env:     []acp.EnvVariable{},
		}})
	}
	for _, spec := range p.opts.mcpStdio {
		name, cmdline, _ := strings.Cut(spec, "=")
		argv := strings.Fields(cmdline)
		if name == "" || len(argv) == 0 {
			continue
		}
		servers = append(servers, acp.McpServer{Stdio: &acp.McpServerStdio{Name: name, Command: argv[0], Args: append([]string{}, argv[1:]...), Env: []acp.EnvVariable{}}})
	}
	return servers
}

// openSession creates, loads, or resumes a session and returns its id.
func (p *prober) openSession(ctx context.Context, pr *proc, loadID string) (acp.SessionId, error) {
	o := p.opts
	meta, err := p.meta()
	if err != nil {
		return "", err
	}
	sctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	switch {
	case loadID != "":
		pf(p.out, "\n--- session/load %s\n", loadID)
		p.client.resetCounters()
		_, err := pr.conn.LoadSession(sctx, acp.LoadSessionRequest{Meta: meta, SessionId: acp.SessionId(loadID), Cwd: p.work, McpServers: p.mcpServers()})
		if err != nil {
			return "", fmt.Errorf("session/load: %w", err)
		}
		n, _ := p.client.counters()
		pf(p.out, "session/load ok; %d session/update notifications replayed\n", n)
		p.sessionResult = pr.wire.result("session/load")
		printSession(p.out, p.sessionResult)
		return acp.SessionId(loadID), nil
	case o.resumeID != "":
		pf(p.out, "\n--- session/resume %s\n", o.resumeID)
		_, err := pr.conn.ResumeSession(sctx, acp.ResumeSessionRequest{Meta: meta, SessionId: acp.SessionId(o.resumeID), Cwd: p.work, McpServers: p.mcpServers()})
		if err != nil {
			return "", fmt.Errorf("session/resume: %w", err)
		}
		p.sessionResult = pr.wire.result("session/resume")
		printSession(p.out, p.sessionResult)
		return acp.SessionId(o.resumeID), nil
	}
	pf(p.out, "\n--- session/new\n")
	t0 := time.Now()
	resp, err := pr.conn.NewSession(sctx, acp.NewSessionRequest{Meta: meta, Cwd: p.work, McpServers: p.mcpServers()})
	if err != nil {
		return "", fmt.Errorf("session/new: %w", err)
	}
	pf(p.out, "session id: %s (%s)\n", resp.SessionId, time.Since(t0).Round(time.Millisecond))
	p.sessionResult = pr.wire.result("session/new")
	printSession(p.out, p.sessionResult)
	return resp.SessionId, nil
}

// configure applies --mode, --model, --config and --raw to an open session.
func (p *prober) configure(ctx context.Context, pr *proc, sid acp.SessionId) error {
	o := p.opts
	cctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if o.mode != "" {
		pf(p.out, "\n--- session/set_mode %s\n", o.mode)
		if _, err := pr.conn.SetSessionMode(cctx, acp.SetSessionModeRequest{SessionId: sid, ModeId: acp.SessionModeId(o.mode)}); err != nil {
			return fmt.Errorf("session/set_mode: %w", err)
		}
		pln(p.out, "ok")
	}
	configs := append(multiFlag(nil), o.configs...)
	if o.model != "" {
		if id, ok := modelConfigID(p.sessionResult); ok {
			configs = append(configs, id+"="+o.model)
		} else {
			pf(p.out, "\n--- session/set_model %s\n", o.model)
			res, err := pr.wire.call(cctx, "session/set_model", map[string]any{"sessionId": sid, "modelId": o.model})
			if err != nil {
				return err
			}
			pf(p.out, "ok %s\n", compactJSON(res))
		}
	}
	for _, kv := range configs {
		id, val, ok := strings.Cut(kv, "=")
		if !ok {
			return fmt.Errorf("--config %q: want id=value", kv)
		}
		pf(p.out, "\n--- session/set_config_option %s=%s\n", id, val)
		_, err := pr.conn.SetSessionConfigOption(cctx, acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{
			SessionId: sid, ConfigId: acp.SessionConfigId(id), Value: acp.SessionConfigValueId(val),
		}})
		if err != nil {
			return fmt.Errorf("session/set_config_option: %w", err)
		}
		pln(p.out, "ok")
	}
	for _, r := range o.raws {
		method, params, ok := strings.Cut(r, "=")
		if !ok {
			return fmt.Errorf("--raw %q: want method=<json>", r)
		}
		params = strings.ReplaceAll(params, "{{sid}}", string(sid))
		if !json.Valid([]byte(params)) {
			return fmt.Errorf("--raw %q: params are not valid JSON", r)
		}
		pf(p.out, "\n--- raw %s %s\n", method, params)
		res, err := pr.wire.call(cctx, method, json.RawMessage(params))
		if err != nil {
			pf(p.out, "error: %v\n", err)
			continue
		}
		pf(p.out, "result: %s\n", prettyJSON(res))
	}
	return nil
}

// prompt sends one prompt, waits for the stop reason, and prints the agent's reply.
func (p *prober) prompt(ctx context.Context, pr *proc, sid acp.SessionId, text string) (string, error) {
	pf(p.out, "\n--- session/prompt\n%s\n--- streaming\n", text)
	p.client.resetCounters()
	pctx, cancel := context.WithTimeout(ctx, p.opts.timeout)
	defer cancel()
	t0 := time.Now()
	resp, err := pr.conn.Prompt(pctx, acp.PromptRequest{SessionId: sid, Prompt: []acp.ContentBlock{acp.TextBlock(text)}})
	if err != nil {
		if pctx.Err() != nil {
			pln(p.out, "!!! prompt timed out or interrupted; sending session/cancel")
			cc, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel2()
			_ = pr.conn.Cancel(cc, acp.CancelNotification{SessionId: sid})
		}
		return "", fmt.Errorf("session/prompt: %w", err)
	}
	reply := p.client.takeText()
	_, thoughts := p.client.counters()
	pf(p.out, "--- stopReason=%s after %s (%d thought chunks)\nagent reply: %s\n", resp.StopReason, time.Since(t0).Round(time.Millisecond), thoughts, strings.TrimSpace(reply))
	return reply, nil
}

func (p *prober) single(ctx context.Context) error {
	pr, err := p.start(ctx)
	if err != nil {
		return err
	}
	defer pr.stop()
	if err := p.initialize(ctx, pr); err != nil {
		return err
	}
	if p.opts.list {
		lctx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		pf(p.out, "\n--- session/list\n")
		if _, err := pr.conn.ListSessions(lctx, acp.ListSessionsRequest{}); err != nil {
			pf(p.out, "error: %v\n", err)
		} else {
			pln(p.out, prettyJSON(pr.wire.result("session/list")))
		}
	}
	sid, err := p.openSession(ctx, pr, p.opts.loadID)
	if err != nil {
		return err
	}
	if err := p.configure(ctx, pr, sid); err != nil {
		return err
	}
	for _, text := range p.prompts {
		if _, err := p.prompt(ctx, pr, sid, text); err != nil {
			return err
		}
	}
	return nil
}

// resumeCheck proves whether session/load restores conversation context: it
// tells the agent a random secret, kills the process, starts a new one, loads
// the session, and asks for the secret without repeating it.
func (p *prober) resumeCheck(ctx context.Context) error {
	secret := fmt.Sprintf("kumquat-%d", time.Now().UnixNano()%1_000_000)
	pr, err := p.start(ctx)
	if err != nil {
		return err
	}
	if err := p.initialize(ctx, pr); err != nil {
		pr.stop()
		return err
	}
	sid, err := p.openSession(ctx, pr, "")
	if err != nil {
		pr.stop()
		return err
	}
	if err := p.configure(ctx, pr, sid); err != nil {
		pr.stop()
		return err
	}
	if _, err := p.prompt(ctx, pr, sid, "Remember this secret word for later: "+secret+". Do not use any tools. Reply with only: OK"); err != nil {
		pr.stop()
		return err
	}
	pr.stop()
	pf(p.out, "\n=== agent process stopped; starting a new process and loading session %s\n", sid)

	pr2, err := p.start(ctx)
	if err != nil {
		return err
	}
	defer pr2.stop()
	if err := p.initialize(ctx, pr2); err != nil {
		return err
	}
	if _, err := p.openSession(ctx, pr2, string(sid)); err != nil {
		pf(p.out, "RESUME-CHECK RESULT: session/load FAILED: %v\n", err)
		return nil
	}
	if err := p.configure(ctx, pr2, sid); err != nil {
		return err
	}
	reply, err := p.prompt(ctx, pr2, sid, "What was the secret word I asked you to remember? Do not use any tools. Reply with only the word.")
	if err != nil {
		return err
	}
	if strings.Contains(strings.ToLower(reply), secret) {
		pf(p.out, "RESUME-CHECK RESULT: PASS: session/load restored context (reply contains %q)\n", secret)
	} else {
		pf(p.out, "RESUME-CHECK RESULT: FAIL: session/load succeeded but the reply does not contain %q\n", secret)
	}
	return nil
}

func (p *prober) summary() {
	c := p.client
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	pf(p.out, "\n=== summary\n")
	pf(p.out, "tool calls seen: %d\n", len(c.order))
	for _, id := range c.order {
		t := c.tools[id]
		pf(p.out, "  %s kind=%s status=%s title=%q rawInput=%s\n", t.ID, t.Kind, t.Status, t.Title, trunc(t.RawInput, 300))
	}
	pf(p.out, "permission requests: %d (policy %s)\n", len(c.perms), c.policy)
	for _, r := range c.perms {
		pf(p.out, "  %s kind=%q title=%q rawInput=%s options=%v -> %s\n", r.ToolCallID, r.Kind, r.Title, trunc(r.RawInput, 300), r.Options, r.Decision)
	}
	if len(c.elicitations) > 0 {
		pf(p.out, "elicitation requests: %d %v\n", len(c.elicitations), c.elicitations)
	}
	if p.opts.mcpEcho {
		if b, err := os.ReadFile(p.mcpLog); err == nil {
			pf(p.out, "mcp echo tool executed:\n%s", b)
		} else {
			pln(p.out, "mcp echo tool executed: never")
		}
	}
	if p.opts.cwd == "" {
		entries, _ := os.ReadDir(p.work)
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		pf(p.out, "work dir files: %v\n", names)
		if b, err := os.ReadFile(filepath.Join(p.work, "note.txt")); err == nil {
			pf(p.out, "note.txt written: %q\n", string(b))
		}
	}
}
