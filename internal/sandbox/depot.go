package sandbox

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"hal9001/internal/spec"
)

// Credential variables of the depot backend. DEPOT_ORG_ID is not secret but is not passed to commands either.
const (
	envDepotToken = "DEPOT_TOKEN"
	envDepotOrg   = "DEPOT_ORG_ID"
)

const defaultDepotEndpoint = "https://api.depot.dev"

// depotBackend drives Depot sandboxes through the depot.sandbox.v1 Connect API (JSON encoding). Commands run
// through RunCommand; files through small shell scripts (see remoteSession), so only unary and
// server-streaming calls are needed.
type depotBackend struct {
	cfg     spec.Sandbox
	cli     *depotClient
	env     map[string]string
	timeout time.Duration

	mu         sync.Mutex
	persistent string // id of the kept-alive sandbox
}

func newDepotBackend(cfg spec.Sandbox, full, cmdEnv map[string]string) *depotBackend {
	org := cfg.OrgID
	if org == "" {
		org = full[envDepotOrg]
	}
	endpoint := orDefault(cfg.Endpoint, defaultDepotEndpoint)
	return &depotBackend{
		cfg: cfg, cli: &depotClient{endpoint: strings.TrimRight(endpoint, "/"), token: full[envDepotToken], org: org, http: &http.Client{}},
		env: cmdEnv, timeout: cfg.Timeout.Std(),
	}
}

func (b *depotBackend) check(ctx context.Context) CheckResult {
	if b.cli.token == "" {
		return CheckResult{Message: "no Depot token: set " + envDepotToken + " in the sandbox environment"}
	}
	var out struct {
		Sandboxes []json.RawMessage `json:"sandboxes"`
	}
	if err := b.cli.unary(ctx, "ListSandboxes", map[string]any{"pageSize": 1}, &out); err != nil {
		return CheckResult{Message: "Depot API: " + err.Error()}
	}
	msg := "Depot API reachable, token accepted"
	if b.cli.org != "" {
		msg += " for organization " + b.cli.org
	}
	return CheckResult{OK: true, Message: msg}
}

func (b *depotBackend) createBody(runID string) map[string]any {
	body := map[string]any{"name": "hal-" + runID}
	res := map[string]any{}
	if b.cfg.Resources.CPUs > 0 {
		res["vcpus"] = int(b.cfg.Resources.CPUs)
	}
	if b.cfg.Resources.MemoryMB > 0 {
		res["memoryMb"] = b.cfg.Resources.MemoryMB
	}
	if b.cfg.Resources.DiskGB > 0 {
		res["diskGb"] = b.cfg.Resources.DiskGB
	}
	if len(res) > 0 {
		body["resources"] = res
	}
	if b.cfg.ImageRef != "" {
		body["runtime"] = map[string]any{"imageRef": b.cfg.ImageRef}
	}
	switch b.cfg.Architecture {
	case "amd64":
		body["architecture"] = 1
	case "arm64":
		body["architecture"] = 2
	}
	if b.cfg.TimeoutMinutes > 0 {
		body["timeoutMinutes"] = b.cfg.TimeoutMinutes
	}
	if b.cfg.DisableTailnet {
		body["disableTailnet"] = true
	}
	if b.cfg.DisableDepotCache {
		body["disableDepotCache"] = true
	}
	if len(b.cfg.CISecrets) > 0 {
		var secrets []map[string]string
		for env, secret := range b.cfg.CISecrets {
			secrets = append(secrets, map[string]string{"env": env, "secret": secret})
		}
		body["configuration"] = map[string]any{"secrets": secrets}
	}
	return body
}

type depotSandbox struct {
	ID     string `json:"sandboxId"`
	Status any    `json:"status"`
}

func (s depotSandbox) state() string {
	switch v := s.Status.(type) {
	case float64:
		return [...]string{"", "created", "assigned", "starting", "running", "finished", "cancelled", "failed"}[min(int(v), 7)]
	case string:
		return strings.ToLower(v[strings.LastIndex(v, "_")+1:])
	}
	return ""
}

func (b *depotBackend) create(ctx context.Context, runID string) (string, error) {
	var out struct {
		Sandbox depotSandbox `json:"sandbox"`
	}
	if err := b.cli.unary(ctx, "CreateSandbox", b.createBody(runID), &out); err != nil {
		return "", fmt.Errorf("create depot sandbox: %w", err)
	}
	id := out.Sandbox.ID
	deadline := time.Now().Add(5 * time.Minute)
	for st := out.Sandbox.state(); st != "running"; {
		switch st {
		case "finished", "cancelled", "failed":
			return "", fmt.Errorf("depot sandbox %s ended while starting (%s)", id, st)
		}
		if time.Now().After(deadline) {
			_ = b.stop(ctx, id)
			return "", fmt.Errorf("depot sandbox %s did not start in time", id)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(time.Second):
		}
		var got struct {
			Sandbox depotSandbox `json:"sandbox"`
		}
		if err := b.cli.unary(ctx, "GetSandbox", map[string]any{"id": id}, &got); err != nil {
			return "", err
		}
		st = got.Sandbox.state()
	}
	return id, nil
}

func (b *depotBackend) stop(ctx context.Context, id string) error {
	return b.cli.unary(ctx, "StopSandbox", map[string]any{"sandbox": map[string]any{"id": id}, "blocking": false}, nil)
}

// persistentSandbox returns the kept-alive sandbox, creating it when it is missing or ended, and extends its
// lifetime.
func (b *depotBackend) persistentSandbox(ctx context.Context) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.persistent != "" {
		var got struct {
			Sandbox depotSandbox `json:"sandbox"`
		}
		if err := b.cli.unary(ctx, "GetSandbox", map[string]any{"id": b.persistent}, &got); err == nil && got.Sandbox.state() == "running" {
			if b.cfg.TimeoutMinutes > 0 {
				_ = b.cli.unary(ctx, "SetSandboxTimeout", map[string]any{
					"sandbox": map[string]any{"id": b.persistent}, "timeoutMinutes": b.cfg.TimeoutMinutes,
				}, nil)
			}
			return b.persistent, nil
		}
		b.persistent = ""
	}
	id, err := b.create(ctx, "persistent")
	if err != nil {
		return "", err
	}
	b.persistent = id
	return id, nil
}

func (b *depotBackend) Prepare(ctx context.Context, rs RunSpec) (Session, error) {
	if !spec.ValidID(rs.RunID) {
		return nil, fmt.Errorf("invalid run id %q", rs.RunID)
	}
	perRun := orDefault(rs.Lifecycle, b.cfg.Lifecycle) == spec.LifecyclePerRun
	var id string
	var err error
	if perRun {
		id, err = b.create(ctx, rs.RunID)
	} else {
		id, err = b.persistentSandbox(ctx)
	}
	if err != nil {
		return nil, err
	}
	env := mergeEnv(b.env, rs.Env)
	run := func(ctx context.Context, script string, _ []byte, timeout time.Duration, limit int) (rawResult, error) {
		return b.cli.runCommand(ctx, id, script, env, timeout, limit)
	}
	res, err := run(ctx, `d="$HOME/hal/`+rs.RunID+`" && mkdir -p "$d" && cd "$d" && pwd -P`, nil, time.Minute, 4096)
	if err != nil || res.ExitCode != 0 {
		if perRun {
			_ = b.stop(ctx, id)
		}
		if err == nil {
			err = fmt.Errorf("create workdir: %s", firstLine(string(res.Stderr)))
		}
		return nil, err
	}
	workdir := strings.TrimSpace(string(res.Stdout))
	s := &remoteSession{
		workdir: workdir, run: run, timeout: b.timeout, noStdin: true, b64Read: true,
		closeFn: func(ctx context.Context, success bool) error {
			switch {
			case perRun:
				// A failed run's sandbox is stopped as well: it holds files only until it expires, and
				// a running one is billed.
				return b.stop(ctx, id)
			case success:
				_, err := run(ctx, "rm -rf -- "+shQuote(workdir), nil, time.Minute, 4096)
				return err
			}
			return nil
		},
	}
	if err := seed(ctx, s, rs); err != nil {
		_ = s.Close(ctx, false)
		return nil, err
	}
	return s, nil
}

// ---- Connect client ----

type depotClient struct {
	endpoint, token, org string
	http                 *http.Client
}

const depotService = "/depot.sandbox.v1.SandboxService/"

// DepotError is an error status returned by the Depot API.
type DepotError struct {
	Code, Message string
	HTTPStatus    int
}

func (e *DepotError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("%s (HTTP %d)", e.Code, e.HTTPStatus)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (c *depotClient) request(ctx context.Context, method, contentType string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+depotService+method, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("Authorization", "Bearer "+c.token)
	if c.org != "" {
		req.Header.Set("x-depot-org", c.org)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		de := &DepotError{Code: http.StatusText(resp.StatusCode), HTTPStatus: resp.StatusCode}
		var e struct{ Code, Message string }
		if json.Unmarshal(b, &e) == nil && e.Code != "" {
			de.Code, de.Message = e.Code, e.Message
		}
		return nil, de
	}
	return resp, nil
}

func (c *depotClient) unary(ctx context.Context, method string, req, out any) error {
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	resp, err := c.request(ctx, method, "application/json", body)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// runCommand runs script with /bin/sh -c in the sandbox and collects the output.
func (c *depotClient) runCommand(ctx context.Context, sandboxID, script string, env map[string]string, timeout time.Duration, limit int) (rawResult, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	payload, err := json.Marshal(map[string]any{
		"sandbox": map[string]any{"id": sandboxID}, "cmd": "/bin/sh", "args": []string{"-c", script}, "env": env,
	})
	if err != nil {
		return rawResult{}, err
	}
	env5 := make([]byte, 5, 5+len(payload))
	binary.BigEndian.PutUint32(env5[1:], uint32(len(payload))) //nolint:gosec // request is small
	resp, err := c.request(ctx, "RunCommand", "application/connect+json", append(env5, payload...))
	var out, errb limitBuffer
	out.limit, errb.limit = limit, limit
	res := func() rawResult {
		return rawResult{Stdout: out.Bytes(), Stderr: errb.Bytes(), Truncated: out.wasTruncated() || errb.wasTruncated()}
	}
	if err != nil {
		if ctx.Err() != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return rawResult{TimedOut: true, ExitCode: -1}, nil
		}
		return rawResult{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	br := bufio.NewReader(resp.Body)
	finished := false
	exit := 0
	for {
		var hdr [5]byte
		if _, err := io.ReadFull(br, hdr[:]); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return c.streamErr(ctx, res(), err)
		}
		msg := make([]byte, binary.BigEndian.Uint32(hdr[1:]))
		if _, err := io.ReadFull(br, msg); err != nil {
			return c.streamErr(ctx, res(), err)
		}
		if hdr[0]&2 != 0 { // end of stream
			var end struct {
				Error *struct{ Code, Message string }
			}
			if json.Unmarshal(msg, &end) == nil && end.Error != nil {
				return res(), &DepotError{Code: end.Error.Code, Message: end.Error.Message}
			}
			break
		}
		var ev struct {
			Stdout   *struct{ Data []byte }
			Stderr   *struct{ Data []byte }
			Finished *struct {
				ExitCode int `json:"exitCode"`
			}
			Error *struct{ Reason string }
		}
		if err := json.Unmarshal(msg, &ev); err != nil {
			return res(), fmt.Errorf("depot: bad command event: %w", err)
		}
		switch {
		case ev.Stdout != nil:
			_, _ = out.Write(ev.Stdout.Data)
		case ev.Stderr != nil:
			_, _ = errb.Write(ev.Stderr.Data)
		case ev.Finished != nil:
			finished, exit = true, ev.Finished.ExitCode
		case ev.Error != nil:
			return res(), fmt.Errorf("depot command failed: %s", ev.Error.Reason)
		}
	}
	if !finished {
		return c.streamErr(ctx, res(), errors.New("command stream ended without a result"))
	}
	r := res()
	r.ExitCode = exit
	return r, nil
}

func (c *depotClient) streamErr(ctx context.Context, r rawResult, err error) (rawResult, error) {
	if ctx.Err() != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		r.TimedOut, r.ExitCode = true, -1
		return r, nil
	}
	return r, err
}
