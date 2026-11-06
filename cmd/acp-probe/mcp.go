package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"
)

// mcpEchoArgs is the argv tail that starts the internal echo MCP server.
func mcpEchoArgs(logPath string, delay time.Duration) []string {
	args := []string{"mcp-echo", logPath}
	if delay > 0 {
		args = append(args, delay.String())
	}
	return args
}

// runMCPEcho is a minimal stdio MCP server exposing one tool, "echo". The probe
// passes it to session/new so we can observe whether an MCP tool call reaches
// session/request_permission. Every tools/call is appended to logPath (when
// set) so a run proves the tool really executed. A non-zero delay holds each
// tools/call reply back that long (the "replied" log line records when it was
// sent), to show how a runtime treats a tool call that blocks, e.g. while a
// harness-side approval is pending.
func runMCPEcho(logPath string, delay time.Duration, in io.Reader, out io.Writer) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	enc := json.NewEncoder(out)
	reply := func(id json.RawMessage, result any) error {
		return enc.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	}
	appendLog := func(line string) {
		if logPath == "" {
			return
		}
		if f, err := os.OpenFile(logPath, //nolint:gosec // operator-chosen log path
			os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			_, _ = fmt.Fprintf(f, "%s %s\n", time.Now().UTC().Format(time.RFC3339Nano), line)
			_ = f.Close()
		}
	}
	for sc.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			continue
		}
		if len(req.ID) == 0 {
			continue // notification
		}
		switch req.Method {
		case "initialize":
			var p struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			_ = json.Unmarshal(req.Params, &p)
			if p.ProtocolVersion == "" {
				p.ProtocolVersion = "2025-06-18"
			}
			err := reply(req.ID, map[string]any{
				"protocolVersion": p.ProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "probe-echo", "version": "0"},
			})
			if err != nil {
				return err
			}
		case "tools/list":
			err := reply(req.ID, map[string]any{"tools": []any{map[string]any{
				"name":        "echo",
				"description": "Returns the given text unchanged. Harmless diagnostic tool.",
				"inputSchema": map[string]any{
					"type":       "object",
					"properties": map[string]any{"text": map[string]any{"type": "string"}},
					"required":   []string{"text"},
				},
			}}})
			if err != nil {
				return err
			}
		case "tools/call":
			var p struct {
				Arguments struct {
					Text string `json:"text"`
				} `json:"arguments"`
			}
			_ = json.Unmarshal(req.Params, &p)
			appendLog(fmt.Sprintf("echo %q", p.Arguments.Text))
			if delay > 0 {
				time.Sleep(delay)
				appendLog(fmt.Sprintf("echo %q replied after %s", p.Arguments.Text, delay))
			}
			err := reply(req.ID, map[string]any{"content": []any{map[string]any{"type": "text", "text": "echo: " + p.Arguments.Text}}})
			if err != nil {
				return err
			}
		case "ping":
			if err := reply(req.ID, map[string]any{}); err != nil {
				return err
			}
		default:
			err := enc.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32601, "message": "method not found"}})
			if err != nil {
				return err
			}
		}
	}
	return sc.Err()
}
