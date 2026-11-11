package runtimes

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"hal9001/internal/spec"
)

// installTimeout bounds one npm install.
const installTimeout = 10 * time.Minute

// Subscribe registers fn for install progress events; the returned function removes it. fn runs on the install's
// goroutine and must not block.
func (m *Manager) Subscribe(fn func(Event)) (unsubscribe func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := m.nextSub
	m.nextSub++
	m.subs[id] = fn
	return func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		delete(m.subs, id)
	}
}

func (m *Manager) emit(ev Event) {
	m.mu.Lock()
	fns := make([]func(Event), 0, len(m.subs))
	for _, fn := range m.subs {
		fns = append(fns, fn)
	}
	m.mu.Unlock()
	for _, fn := range fns {
		fn(ev)
	}
}

// adapterOf returns the adapter hal9001 installs for rt, or ErrNotInstallable.
func adapterOf(rt spec.Runtime) (*adapterInfo, error) {
	if len(rt.Command) > 0 {
		return nil, fmt.Errorf("%w: it runs the command set in config.yaml", ErrNotInstallable)
	}
	info, ok := kinds[kindOf(rt)]
	if !ok || info.adapter == nil {
		if ok {
			return nil, fmt.Errorf("%w: %s speaks ACP itself (`%s acp`)", ErrNotInstallable, info.title, info.cli)
		}
		return nil, ErrNotInstallable
	}
	return info.adapter, nil
}

// Install starts installing the pinned ACP adapter of runtime name into <data>/adapters/<adapter> with npm. It
// returns at once; progress arrives as Events (the last one is PhaseDone or PhaseFailed) and the cached detections
// are dropped when the install ends. Errors: ErrNotInstallable, ErrBusy, *PrerequisiteError (Node.js or npm
// missing or too old).
func (m *Manager) Install(ctx context.Context, name string, rt spec.Runtime) error {
	ai, err := adapterOf(rt)
	if err != nil {
		return err
	}
	if m.isInstalling(ai) {
		return ErrBusy
	}
	node, npm := m.lookup(ctx, "node"), m.lookup(ctx, "npm")
	switch {
	case !node.found:
		return &PrerequisiteError{Code: CodeNodeMissing, Message: fmt.Sprintf("Node.js %d or newer was not found on PATH; install it first.", MinNodeMajor)}
	case nodeMajor(node.version) < MinNodeMajor:
		return &PrerequisiteError{Code: CodeNodeOld, Message: fmt.Sprintf("Node.js %s was found, but the adapter needs %d or newer.", node.version, MinNodeMajor)}
	case !npm.found:
		return &PrerequisiteError{Code: CodeNodeMissing, Message: "npm was not found on PATH; install Node.js with npm first."}
	}
	m.mu.Lock()
	if m.installing[ai.bin] {
		m.mu.Unlock()
		return ErrBusy
	}
	m.installing[ai.bin] = true
	delete(m.lastErr, ai.bin)
	m.mu.Unlock()
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		m.runInstall(name, ai, node, npm)
	}()
	return nil
}

func (m *Manager) runInstall(name string, ai *adapterInfo, node, npm tool) {
	err := m.npmInstall(name, ai, node, npm)
	m.mu.Lock()
	delete(m.installing, ai.bin)
	if err != nil {
		m.lastErr[ai.bin] = err.Error()
	}
	m.mu.Unlock()
	m.Refresh()
	if err != nil {
		m.log.Warn("runtimes: adapter install failed", "runtime", name, "adapter", ai.bin, "err", err)
		m.emit(Event{Name: name, Phase: PhaseFailed, Error: err.Error()})
		return
	}
	m.log.Info("runtimes: adapter installed", "runtime", name, "adapter", ai.bin, "version", ai.pin)
	m.emit(Event{Name: name, Phase: PhaseDone})
}

func (m *Manager) npmInstall(name string, ai *adapterInfo, node, npm tool) error {
	dir := filepath.Join(m.opt.Data, "adapters", ai.bin)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(m.ctx, installTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, npm.path, "install", "--prefix", dir, ai.pkg+"@"+ai.pin, "--no-audit", "--no-fund") //nolint:gosec // fixed catalog package and pin
	cmd.Dir = m.opt.Data
	env := m.env()
	env["PATH"] = filepath.Dir(node.path) + string(os.PathListSeparator) + env["PATH"] // npm's shebang finds this node
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.WaitDelay = 5 * time.Second
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	var tail ring
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			if len(line) > 500 {
				line = line[:500]
			}
			tail.add(line)
			m.emit(Event{Name: name, Phase: PhaseInstalling, Line: line})
		}
		_, _ = io.Copy(io.Discard, pr)
	}()
	m.emit(Event{Name: name, Phase: PhaseInstalling, Line: "npm install " + ai.pkg + "@" + ai.pin})
	runErr := cmd.Run()
	_ = pw.Close()
	<-done
	if _, _, ok := m.readAdapter(ai); runErr == nil && !ok {
		runErr = errors.New("npm finished but the adapter's entry script is missing")
	}
	if runErr != nil {
		if _, _, ok := m.readAdapter(ai); !ok {
			_ = os.RemoveAll(dir) // never leave a half install behind
		}
		msg := runErr.Error()
		if ctx.Err() != nil {
			msg = "install was interrupted or timed out"
		}
		if t := tail.String(); t != "" {
			msg += ": " + t
		}
		return errors.New(msg)
	}
	return nil
}

// Uninstall removes the managed adapter of runtime name. Errors: ErrNotInstallable, ErrBusy.
func (m *Manager) Uninstall(name string, rt spec.Runtime) error {
	ai, err := adapterOf(rt)
	if err != nil {
		return err
	}
	m.mu.Lock()
	if m.installing[ai.bin] {
		m.mu.Unlock()
		return ErrBusy
	}
	delete(m.lastErr, ai.bin)
	m.mu.Unlock()
	if err := os.RemoveAll(filepath.Join(m.opt.Data, "adapters", ai.bin)); err != nil {
		return err
	}
	m.Refresh()
	m.log.Info("runtimes: adapter removed", "runtime", name, "adapter", ai.bin)
	return nil
}

// ring keeps the last few lines of npm's output for the failure message.
type ring struct{ lines []string }

func (r *ring) add(s string) {
	r.lines = append(r.lines, s)
	if len(r.lines) > 6 {
		r.lines = r.lines[1:]
	}
}

func (r *ring) String() string { return strings.Join(r.lines, " | ") }
