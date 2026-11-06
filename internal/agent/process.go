package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// process is a spawned agent in its own process group.
type process struct {
	cmd    *exec.Cmd
	stdin  *os.File // our write end of the agent's stdin
	stdout *os.File // our read end of the agent's stdout
	stderr *tailBuffer
	done   chan struct{} // closed once the leader was reaped
	// stderrDone is closed when every holder of the stderr pipe closed it.
	stderrDone chan struct{}
	pgid       int
	// updates accounts for the session/update notifications read from stdout (see updateGate).
	updates *updateGate
	// eof, when set, is closed once the ACP connection has read stdout to EOF.
	eof <-chan struct{}
}

// startProcess runs argv in dir with env in a new process group. The agent's
// stdin/stdout are plain pipes owned by us, so the leader can be reaped
// without truncating buffered output.
func startProcess(argv []string, dir string, env []string) (*process, error) {
	if len(argv) == 0 {
		return nil, errors.New("runtime command is empty")
	}
	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		_ = inR.Close()
		_ = inW.Close()
		return nil, err
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		for _, f := range []*os.File{inR, inW, outR, outW} {
			_ = f.Close()
		}
		return nil, err
	}
	p := &process{stderr: &tailBuffer{max: 64 << 10}, done: make(chan struct{}), stderrDone: make(chan struct{})}
	cmd := exec.CommandContext(context.Background(), argv[0], argv[1:]...) //nolint:gosec // argv is the configured runtime command
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdin = inR
	cmd.Stdout = outW
	cmd.Stderr = errW
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		for _, f := range []*os.File{inR, inW, outR, outW, errR, errW} {
			_ = f.Close()
		}
		return nil, err
	}
	// The child holds its ends; close ours so EOF propagates.
	_ = inR.Close()
	_ = outW.Close()
	_ = errW.Close()
	go func() {
		defer close(p.stderrDone)
		defer func() { _ = errR.Close() }()
		_, _ = io.Copy(p.stderr, errR)
	}()
	p.cmd, p.stdin, p.stdout, p.pgid = cmd, inW, outR, cmd.Process.Pid
	go func() {
		_ = cmd.Wait()
		close(p.done)
	}()
	return p, nil
}

func (p *process) signalGroup(sig syscall.Signal) {
	// ESRCH (group already gone) is the expected outcome after a clean exit.
	_ = syscall.Kill(-p.pgid, sig)
}

func (p *process) waitExit(d time.Duration) bool {
	if d <= 0 {
		select {
		case <-p.done:
			return true
		default:
			return false
		}
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-p.done:
		return true
	case <-t.C:
		return false
	}
}

// stop ends the process and everything in its group: close stdin and give the
// leader exitWait to leave by itself, then SIGTERM, then SIGKILL after
// killGrace. The group always receives a final SIGKILL so stragglers (tool
// subprocesses) do not outlive the run.
func (p *process) stop(exitWait, killGrace time.Duration) {
	_ = p.stdin.Close()
	if !p.waitExit(exitWait) {
		p.signalGroup(syscall.SIGTERM)
		if !p.waitExit(killGrace) {
			p.signalGroup(syscall.SIGKILL)
			<-p.done
		}
	}
	p.signalGroup(syscall.SIGKILL)
	// Every writer is gone, so the connection reaches EOF at once. Closing our read end before it did would drop
	// output the agent wrote just before it exited.
	if p.eof != nil {
		t := time.NewTimer(time.Second)
		select {
		case <-p.eof:
		case <-t.C:
		}
		t.Stop()
	}
	_ = p.stdout.Close()
	// The kill closed every writer, so the stderr tail is complete shortly.
	t := time.NewTimer(time.Second)
	defer t.Stop()
	select {
	case <-p.stderrDone:
	case <-t.C:
	}
}

// exitCode is -1 when the process was killed by a signal.
func (p *process) exitCode() int {
	if p.cmd.ProcessState == nil {
		return -1
	}
	return p.cmd.ProcessState.ExitCode()
}

// tailBuffer keeps the last max bytes written.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (t *tailBuffer) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, b...)
	if len(t.buf) > t.max {
		t.buf = append([]byte(nil), t.buf[len(t.buf)-t.max:]...)
	}
	return len(b), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

// lineSplitter reports complete lines (without the newline) fed in pieces.
type lineSplitter struct {
	buf  []byte
	emit func(line []byte)
}

func (l *lineSplitter) feed(b []byte) {
	l.buf = append(l.buf, b...)
	for {
		i := bytes.IndexByte(l.buf, '\n')
		if i < 0 {
			return
		}
		if line := bytes.TrimRight(l.buf[:i], "\r"); len(line) > 0 {
			l.emit(append([]byte(nil), line...))
		}
		l.buf = l.buf[i+1:]
	}
}

// teeWriter forwards writes and reports each complete JSON-RPC line.
type teeWriter struct {
	w  io.Writer
	mu sync.Mutex
	ls lineSplitter
}

func newTeeWriter(w io.Writer, emit func([]byte)) *teeWriter {
	return &teeWriter{w: w, ls: lineSplitter{emit: emit}}
}

func (t *teeWriter) Write(b []byte) (int, error) {
	n, err := t.w.Write(b)
	t.mu.Lock()
	t.ls.feed(b[:n])
	t.mu.Unlock()
	return n, err
}

// teeReader reports each complete JSON-RPC line read from the agent.
type teeReader struct {
	r  io.Reader
	ls lineSplitter // only touched by the single reading goroutine
}

func newTeeReader(r io.Reader, emit func([]byte)) *teeReader {
	return &teeReader{r: r, ls: lineSplitter{emit: emit}}
}

func (t *teeReader) Read(b []byte) (int, error) {
	n, err := t.r.Read(b)
	if n > 0 {
		t.ls.feed(b[:n])
	}
	return n, err
}

// buildEnv layers the base environment, the runtime env, the policy-specific
// env and per-run extras (later entries win).
func buildEnv(base []string, layers ...map[string]string) []string {
	env := append([]string(nil), base...)
	for _, m := range layers {
		for k, v := range m {
			env = append(env, k+"="+v)
		}
	}
	return env
}

func describeExit(p *process) string {
	if p == nil || p.cmd == nil || p.cmd.ProcessState == nil {
		return "agent process still running"
	}
	return fmt.Sprintf("agent process exited: %s", p.cmd.ProcessState)
}
