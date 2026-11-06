// Package sandbox runs agent work in an isolated workspace: commands and file operations confined to a
// workdir, on one of five backends (local, docker, depot, kubernetes, ssh). The configuration of each backend
// is a spec.Sandbox (sandboxes/<backend>.yaml) and its environment lives in env.d/sandboxes/<backend>.env.
//
// # Using it (the tool layer)
//
//	env, _ := sandbox.LoadEnv(home, "docker")          // global env overlaid by the sandbox env
//	b, err := sandbox.New("docker", cfg, env, home)    // cfg is a spec.Sandbox
//	sess, err := b.Prepare(ctx, sandbox.RunSpec{
//		RunID: runID, Lifecycle: cfg.Lifecycle, Image: "ubuntu:24.04",
//		Repo: "https://github.com/o/r", Ref: "main", GitToken: sandbox.GitHubToken(ctx),
//		Input: inputJSON, Env: extra,
//	})
//	res, err := sess.Exec(ctx, "go test ./...", nil, 0)  // res.Stdout, res.Stderr, res.ExitCode, res.TimedOut
//	data, err := sess.ReadFile(ctx, "main.go")
//	err = sess.WriteFile(ctx, "out/report.md", data)
//	ents, err := sess.List(ctx, ".")
//	hits, err := sess.Search(ctx, ".", `TODO\(`)
//	err = sess.Close(ctx, success)                       // per_run + success removes the workspace
//
// Backends that hold a connection (ssh) implement io.Closer; call it when the backend is discarded (for
// example after the config changed). A Backend is safe for concurrent use; so is a Session.
//
// # Contract
//
//   - Prepare creates the workdir (per_run: one per run; persistent: reused, with a per-run directory on
//     docker, kubernetes and depot), seeds it (optional git clone of Repo at Ref, then input.json) and returns
//     a Session. The clone token reaches only the clone command.
//   - File paths are relative to Session.Workdir (absolute paths inside it are accepted). A path that leaves
//     the workdir lexically (..) or through a symlink fails with ErrEscape; missing files wrap
//     fs.ErrNotExist (ErrNotFound); files above MaxFileSize fail with ErrTooLarge. Exec output is cut at
//     MaxOutput bytes per stream (ExecResult.Truncated).
//   - Env: commands see hal's global env overlaid by the sandbox env, plus RunSpec.Env. The backend's own
//     credentials (DEPOT_TOKEN, SSH_PASSWORD, SSH_PRIVATE_KEY, ...) are not passed to commands.
//   - Everything returned by a Session (output, file contents, search hits, errors) has secret values
//     replaced by Redacted. Values shorter than 4 bytes are not redacted. Encoded exfiltration is not caught.
//   - Close(ctx, false) keeps a failed per_run workspace for debugging; backends implement Sweeper to remove
//     it after the retention period (call Sweep(ctx, maxAge) on the Backend returned by New).
//
// # Approval
//
// NewPolicy(cfg) compiles a sandbox's approval settings. Policy.Decide(OpShell, {"command": ...}) and
// Decide(OpFSWrite, {"path": ...}) return auto, approve (queue an Inbox approval) or deny. Every other
// operation needs no approval.
//
// # Checking
//
// Check(ctx, backend, cfg, env, home) tests connectivity without leaving state behind. For ssh it returns the
// host key fingerprint when none is pinned (or it changed); the user confirms it and the caller stores it as
// host_key. Prepare on an unpinned host fails with *HostKeyError.
//
// # Backends
//
//   - local: <root>/runs/<run-id> (per_run, removed on success) or <root>; commands run with /bin/sh on this
//     machine. Always enabled (no `enabled` switch, no config file needed): the default root
//     <home>/data/sandboxes/local is created on demand, and the default policy lets fs_write inside the workdir
//     run while shell commands ask.
//   - docker: Engine API on the socket (config, DOCKER_HOST, or a standard socket). One container per run, or
//     one per image kept running; the workdir is /tmp/hal/<run-id>. network:false uses the none network.
//   - depot: depot.sandbox.v1 over Connect+JSON: CreateSandbox per run (StopSandbox at the end) or one kept
//     alive sandbox. DEPOT_TOKEN (required while depot is enabled; the Depot tab writes it to
//     env.d/sandboxes/depot.env, never to depot.yaml) and optionally DEPOT_ORG_ID come from the sandbox env.
//   - kubernetes: kubectl with KUBECONFIG, --context and -n; one pod per run (deleted at the end) or one pod
//     per image; the workdir is /tmp/hal/<run-id>.
//   - ssh: golang.org/x/crypto/ssh; auth by key_file / SSH_PRIVATE_KEY / SSH_PASSWORD; the host key must be
//     pinned. Root with runs/<run-id>.
//
// docker, kubernetes, depot and ssh run files operations as POSIX shell scripts, so the image needs sh,
// grep, base64 (depot), wc and dirname.
package sandbox
