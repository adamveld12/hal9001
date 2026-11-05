package fakeagent

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sync/atomic"

	"github.com/coder/acp-go-sdk"
)

// stuck is set by `hang` with ignore_cancel: the process then also ignores
// stdin EOF, so only a signal ends it.
var stuck atomic.Bool

func marshalUpdate(u acp.SessionUpdate) (json.RawMessage, error) { return json.Marshal(u) }

// Main runs the fake agent over the given stdio and returns the process exit
// code. Flags: --script, --sessions-dir, --log; env FAKE_ACP_SCRIPT,
// FAKE_ACP_SESSIONS, FAKE_ACP_LOG are used when the flags are unset.
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("fake-acp-agent", flag.ContinueOnError)
	fs.SetOutput(stderr)
	scriptPath := fs.String("script", os.Getenv("FAKE_ACP_SCRIPT"), "path to the JSON script")
	sessionsDir := fs.String("sessions-dir", os.Getenv("FAKE_ACP_SESSIONS"), "directory for persisted sessions")
	logPath := fs.String("log", os.Getenv("FAKE_ACP_LOG"), "JSONL file receiving one line per ACP call")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *scriptPath == "" {
		_, _ = fmt.Fprintln(stderr, "fake-acp-agent: --script or FAKE_ACP_SCRIPT is required")
		return 2
	}
	script, err := Load(*scriptPath)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "fake-acp-agent:", err)
		return 2
	}

	pre := &turnState{ctx: context.Background(), emitted: map[string]bool{}}
	if _, err := pre.run(script.Startup); err != nil {
		_, _ = fmt.Fprintln(stderr, "fake-acp-agent: startup:", err)
		return 2
	}

	ag := New(script, *sessionsDir, *logPath)
	conn := acp.NewAgentSideConnection(ag, stdout, stdin)
	ag.(interface {
		SetAgentConnection(*acp.AgentSideConnection)
	}).SetAgentConnection(conn)
	<-conn.Done()
	if stuck.Load() {
		select {} // a hung agent ignores stdin EOF too
	}
	return 0
}
