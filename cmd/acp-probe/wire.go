package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// wireMsg is the subset of a JSON-RPC 2.0 message the probe inspects.
type wireMsg struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  json.RawMessage `json:"error,omitempty"`
}

// wireLog records every JSON-RPC line crossing the agent's stdio, in both
// directions, as JSONL. It also remembers the raw result of the handshake
// methods so the probe can print fields the SDK's typed structs drop, and it
// feeds every agent-originated message to a hook for live display.
type wireLog struct {
	mu      sync.Mutex
	out     io.Writer
	start   time.Time
	pending map[string]string          // request id (raw JSON) -> method, for requests we sent
	results map[string]json.RawMessage // method -> most recent raw result
	onAgent func(wireMsg)
	waiters map[string]chan wireMsg // raw-call id -> response channel
	rawSeq  int
	agentIn io.Writer // the logging writer around the agent's stdin
}

func newWireLog(out io.Writer) *wireLog {
	return &wireLog{
		out:     out,
		start:   time.Now(),
		pending: map[string]string{},
		results: map[string]json.RawMessage{},
		waiters: map[string]chan wireMsg{},
	}
}

type wireRecord struct {
	T   string `json:"t"`
	Dir string `json:"dir"`
	Msg any    `json:"msg"`
}

func (w *wireLog) record(dir string, line []byte) {
	line = bytes.TrimRight(line, "\r\n")
	if len(line) == 0 {
		return
	}
	var msg any
	if json.Valid(line) {
		msg = json.RawMessage(line)
	} else {
		msg = string(line)
	}
	rec, err := json.Marshal(wireRecord{T: time.Now().UTC().Format(time.RFC3339Nano), Dir: dir, Msg: msg})
	if err != nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	_, _ = w.out.Write(append(rec, '\n'))
}

// toAgent logs a line the client sent to the agent.
func (w *wireLog) toAgent(line []byte) {
	w.record("->", line)
	var m wireMsg
	if json.Unmarshal(bytes.TrimSpace(line), &m) != nil {
		return
	}
	if m.Method != "" && len(m.ID) > 0 {
		w.mu.Lock()
		w.pending[string(m.ID)] = m.Method
		w.mu.Unlock()
	}
}

// fromAgent logs a line the agent sent to the client and runs the display hook.
func (w *wireLog) fromAgent(line []byte) {
	w.record("<-", line)
	var m wireMsg
	if json.Unmarshal(bytes.TrimSpace(line), &m) != nil {
		return
	}
	if m.Method == "" && len(m.ID) > 0 {
		w.mu.Lock()
		method := w.pending[string(m.ID)]
		delete(w.pending, string(m.ID))
		if ch, ok := w.waiters[string(m.ID)]; ok {
			delete(w.waiters, string(m.ID))
			ch <- m
		}
		if method != "" && len(m.Result) > 0 {
			w.results[method] = m.Result
		}
		w.mu.Unlock()
		if len(m.Error) > 0 {
			m.Method = "<error response to " + method + ">"
		} else {
			m.Method = "<response to " + method + ">"
		}
	}
	if w.onAgent != nil {
		w.onAgent(m)
	}
}

func (w *wireLog) stderrLine(line string) {
	w.record("stderr", []byte(strings.TrimRight(line, "\r\n")))
}

// result returns the last raw result recorded for method.
func (w *wireLog) result(method string) json.RawMessage {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.results[method]
}

// writer wraps the agent's stdin so each written JSON-RPC line is logged.
func (w *wireLog) writer(dst io.Writer) io.Writer {
	tw := &teeWriter{dst: dst, log: w}
	w.agentIn = tw
	return tw
}

// call sends an arbitrary JSON-RPC request outside the SDK (for methods the SDK
// has no binding for, such as unstable session/set_model) and waits for the
// response. The id is a string so it cannot collide with the SDK's integer ids.
func (w *wireLog) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	w.mu.Lock()
	w.rawSeq++
	id := fmt.Sprintf("%q", fmt.Sprintf("probe-%d", w.rawSeq))
	ch := make(chan wireMsg, 1)
	w.waiters[id] = ch
	w.mu.Unlock()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "method": method, "params": params})
	if err != nil {
		return nil, err
	}
	if _, err := w.agentIn.Write(append(body, '\n')); err != nil {
		return nil, err
	}
	select {
	case m := <-ch:
		if len(m.Error) > 0 {
			return nil, fmt.Errorf("%s: %s", method, compactJSON(m.Error))
		}
		return m.Result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type teeWriter struct {
	mu  sync.Mutex
	dst io.Writer
	log *wireLog
	buf []byte
}

func (t *teeWriter) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	// Log before delivering: a fast agent can answer while this call is still
	// returning, and fromAgent needs the request recorded to attribute the
	// response to its method.
	t.buf = append(t.buf, p...)
	for {
		i := bytes.IndexByte(t.buf, '\n')
		if i < 0 {
			break
		}
		t.log.toAgent(t.buf[:i])
		t.buf = t.buf[i+1:]
	}
	return t.dst.Write(p)
}

// reader wraps the agent's stdout. A goroutine splits it into lines, logs each,
// and forwards the original bytes through a pipe to the SDK connection.
func (w *wireLog) reader(src io.Reader) (io.Reader, <-chan struct{}) {
	pr, pw := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		br := bufio.NewReaderSize(src, 1<<20)
		for {
			line, err := br.ReadBytes('\n')
			if len(line) > 0 {
				w.fromAgent(line)
				if _, werr := pw.Write(line); werr != nil {
					return
				}
			}
			if err != nil {
				_ = pw.CloseWithError(err)
				return
			}
		}
	}()
	return pr, done
}

func compactJSON(raw []byte) string {
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		return string(raw)
	}
	return b.String()
}

func prettyJSON(raw []byte) string {
	var b bytes.Buffer
	if err := json.Indent(&b, raw, "", "  "); err != nil {
		return string(raw)
	}
	return b.String()
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + fmt.Sprintf("...(+%d bytes)", len(s)-n)
}
