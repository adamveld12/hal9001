package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"hal9001/internal/spec"
)

// kubeBackend drives a cluster through the kubectl binary. Pods run an idle shell; commands go through
// `kubectl exec`.
type kubeBackend struct {
	cfg     spec.Sandbox
	env     map[string]string
	kubectl string
	timeout time.Duration

	mu sync.Mutex // serializes creation of persistent pods
}

func newKubeBackend(cfg spec.Sandbox, env map[string]string) *kubeBackend {
	return &kubeBackend{cfg: cfg, env: env, kubectl: "kubectl", timeout: cfg.Timeout.Std()}
}

const kubeWorkRoot = "/tmp/hal"

// kc runs kubectl with the sandbox's kubeconfig, context and namespace.
func (b *kubeBackend) kc(ctx context.Context, stdin []byte, timeout time.Duration, limit int, args ...string) (rawResult, error) {
	var base []string
	if b.cfg.Context != "" {
		base = append(base, "--context", b.cfg.Context)
	}
	if b.cfg.Namespace != "" {
		base = append(base, "-n", b.cfg.Namespace)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	c := exec.CommandContext(ctx, b.kubectl, append(base, args...)...) //nolint:gosec // kubectl with arguments built here
	c.Env = os.Environ()
	if b.cfg.Kubeconfig != "" {
		c.Env = append(c.Env, "KUBECONFIG="+b.cfg.Kubeconfig)
	}
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error { return syscall.Kill(-c.Process.Pid, syscall.SIGKILL) }
	c.WaitDelay = 2 * time.Second
	if stdin != nil {
		c.Stdin = bytes.NewReader(stdin)
	}
	var out, errb limitBuffer
	out.limit, errb.limit = limit, limit
	c.Stdout, c.Stderr = &out, &errb
	err := c.Run()
	res := rawResult{Stdout: out.Bytes(), Stderr: errb.Bytes(), Truncated: out.wasTruncated() || errb.wasTruncated()}
	var ee *exec.ExitError
	switch {
	case ctx.Err() != nil && errors.Is(ctx.Err(), context.DeadlineExceeded):
		res.TimedOut, res.ExitCode = true, -1
	case errors.As(err, &ee):
		res.ExitCode = ee.ExitCode()
	case err != nil && !errors.Is(err, exec.ErrWaitDelay):
		return res, err
	}
	return res, nil
}

func (b *kubeBackend) check(ctx context.Context) CheckResult {
	res, err := b.kc(ctx, nil, 30*time.Second, 1<<16, "version", "-o", "json")
	if err != nil {
		return CheckResult{Message: "cannot run kubectl: " + err.Error()}
	}
	var v struct {
		ClientVersion struct{ GitVersion string } `json:"clientVersion"`
		ServerVersion struct{ GitVersion string } `json:"serverVersion"`
	}
	_ = json.Unmarshal(res.Stdout, &v)
	if res.ExitCode != 0 || v.ServerVersion.GitVersion == "" {
		return CheckResult{Message: "cannot reach the cluster: " + firstLine(string(res.Stderr))}
	}
	msg := fmt.Sprintf("kubectl %s, cluster %s, namespace %s", v.ClientVersion.GitVersion, v.ServerVersion.GitVersion, b.cfg.Namespace)
	can, err := b.kc(ctx, nil, 30*time.Second, 1<<12, "auth", "can-i", "create", "pods")
	if err == nil && strings.TrimSpace(string(can.Stdout)) != "yes" {
		return CheckResult{Message: msg + "; this account cannot create pods in the namespace"}
	}
	return CheckResult{OK: true, Message: msg}
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

var nonDNS = regexp.MustCompile(`[^a-z0-9-]+`)

func podName(prefix, id string) string {
	n := prefix + strings.Trim(nonDNS.ReplaceAllString(strings.ToLower(id), "-"), "-")
	if len(n) > 52 {
		n = n[:44] + "-" + shortHash(id)[:7]
	}
	return strings.TrimRight(n, "-")
}

func (b *kubeBackend) image(rs RunSpec) (string, error) {
	img := rs.Image
	if img == "" && len(b.cfg.Images) > 0 {
		img = b.cfg.Images[0]
	}
	if img == "" {
		return "", errors.New("no kubernetes image configured")
	}
	if !containsStr(b.cfg.Images, img) {
		return "", fmt.Errorf("image %q is not in the sandbox's image list", img)
	}
	return img, nil
}

func (b *kubeBackend) Prepare(ctx context.Context, rs RunSpec) (Session, error) {
	if !spec.ValidID(rs.RunID) {
		return nil, fmt.Errorf("invalid run id %q", rs.RunID)
	}
	img, err := b.image(rs)
	if err != nil {
		return nil, err
	}
	perRun := orDefault(rs.Lifecycle, b.cfg.Lifecycle) == spec.LifecyclePerRun
	name := podName("hal-", rs.RunID)
	if !perRun {
		name = podName("hal-persistent-", shortHash(img))
	}
	b.mu.Lock()
	err = b.ensurePod(ctx, name, img, rs.RunID, !perRun)
	b.mu.Unlock()
	if err != nil {
		return nil, err
	}
	run := func(ctx context.Context, script string, stdin []byte, timeout time.Duration, limit int) (rawResult, error) {
		args := []string{"exec"}
		if stdin != nil {
			args = append(args, "-i")
		}
		args = append(args, name, "--", "/bin/sh", "-c", script)
		return b.kc(ctx, stdin, timeout, limit, args...)
	}
	workdir := kubeWorkRoot + "/" + rs.RunID
	res, err := run(ctx, "mkdir -p "+shQuote(workdir)+" && cd "+shQuote(workdir)+" && pwd -P", nil, time.Minute, 4096)
	if err != nil || res.ExitCode != 0 {
		if perRun {
			_, _ = b.kc(ctx, nil, time.Minute, 4096, "delete", "pod", name, "--wait=false")
		}
		if err == nil {
			err = fmt.Errorf("create workdir: %s", firstLine(string(res.Stderr)))
		}
		return nil, err
	}
	s := &remoteSession{
		workdir: strings.TrimSpace(string(res.Stdout)), run: run, timeout: b.timeout,
		env: mergeEnv(b.env, rs.Env), inlineEnv: true,
		closeFn: func(ctx context.Context, success bool) error {
			switch {
			case perRun && success:
				_, err := b.kc(ctx, nil, time.Minute, 4096, "delete", "pod", name, "--wait=false")
				return err
			case !perRun && success:
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

func (b *kubeBackend) ensurePod(ctx context.Context, name, img, runID string, persistent bool) error {
	res, err := b.kc(ctx, nil, time.Minute, 4096, "get", "pod", name, "-o", "jsonpath={.status.phase}")
	if err != nil {
		return err
	}
	if res.ExitCode == 0 && strings.TrimSpace(string(res.Stdout)) == "Running" {
		return nil
	}
	if res.ExitCode != 0 && !strings.Contains(string(res.Stderr), "NotFound") {
		return fmt.Errorf("kubectl get pod: %s", firstLine(string(res.Stderr)))
	}
	phase := strings.TrimSpace(string(res.Stdout))
	if res.ExitCode == 0 && phase != "Pending" {
		// A finished or failed pod cannot be reused.
		if _, err := b.kc(ctx, nil, time.Minute, 4096, "delete", "pod", name, "--wait=true"); err != nil {
			return err
		}
	}
	if res.ExitCode != 0 || phase != "Pending" {
		labels := "hal9001.sandbox=1,hal9001.run=" + runID
		if persistent {
			labels += ",hal9001.persistent=1"
		}
		args := []string{"run", name, "--image=" + img, "--restart=Never", "--labels=" + labels}
		if ov := b.overrides(name, img); ov != "" {
			args = append(args, "--overrides="+ov)
		}
		args = append(args, "--command", "--", "/bin/sh", "-c", "while :; do sleep 3600; done")
		run, err := b.kc(ctx, nil, 2*time.Minute, 1<<14, args...)
		if err != nil {
			return err
		}
		if run.ExitCode != 0 {
			return fmt.Errorf("kubectl run: %s", firstLine(string(run.Stderr)))
		}
	}
	wait, err := b.kc(ctx, nil, 5*time.Minute, 1<<14, "wait", "--for=condition=Ready", "pod/"+name, "--timeout=240s")
	if err != nil {
		return err
	}
	if wait.ExitCode != 0 {
		return fmt.Errorf("pod %s did not become ready: %s", name, firstLine(string(wait.Stderr)))
	}
	return nil
}

// overrides is the --overrides JSON for the service account and resources, or "" when neither is set.
func (b *kubeBackend) overrides(name, img string) string {
	r := b.cfg.Resources
	if b.cfg.ServiceAccount == "" && r.CPUs == 0 && r.MemoryMB == 0 {
		return ""
	}
	limits := map[string]string{}
	if r.CPUs > 0 {
		limits["cpu"] = fmt.Sprint(r.CPUs)
	}
	if r.MemoryMB > 0 {
		limits["memory"] = fmt.Sprintf("%dMi", r.MemoryMB)
	}
	pod := map[string]any{"containers": []map[string]any{{
		"name": name, "image": img, "resources": map[string]any{"limits": limits, "requests": limits},
	}}}
	if b.cfg.ServiceAccount != "" {
		pod["serviceAccountName"] = b.cfg.ServiceAccount
	}
	out, _ := json.Marshal(map[string]any{"spec": pod})
	return string(out)
}

// Sweep deletes per_run pods older than maxAge (kept after failed runs).
func (b *kubeBackend) Sweep(ctx context.Context, maxAge time.Duration) error {
	res, err := b.kc(ctx, nil, time.Minute, 4<<20, "get", "pods", "-l", "hal9001.sandbox=1,!hal9001.persistent", "-o", "json")
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("kubectl get pods: %s", firstLine(string(res.Stderr)))
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name              string
				CreationTimestamp time.Time
			}
		}
	}
	if err := json.Unmarshal(res.Stdout, &list); err != nil {
		return err
	}
	for _, p := range list.Items {
		if time.Since(p.Metadata.CreationTimestamp) > maxAge {
			_, _ = b.kc(ctx, nil, time.Minute, 4096, "delete", "pod", p.Metadata.Name, "--wait=false")
		}
	}
	return nil
}
