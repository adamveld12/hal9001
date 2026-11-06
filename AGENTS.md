# hal9001: working rules

Read `REQUIREMENTS.md` before changing anything. These rules apply to every human and agent working in this repo.

## Toolchain

- All tools are pinned in `mise.toml`. Run them through `mise exec -- <tool>` (or `mise run <task>`); never rely on globally installed versions.
- First-time setup in a clone or worktree: `mise run setup` (installs git hooks and web deps).
- Full check of the tree: `mise run check`.

## Commits and hooks

- Commit early and often: one logical change per commit, with a message that says what changed and why.
- Never bypass hooks (`--no-verify`, `LEFTHOOK=0`, editing hook config to get a commit through).
- When a hook fails, triage before doing anything else:
  1. **Assume the code is wrong first.** Read the failure, fix the code, commit again.
  2. If the failure is clearly the tooling (a crash in the tool itself, a false positive that can't be fixed in code, a broken environment, a hook that can't run in a worktree), **stop and report it** to the coordinator/user with the exact command, output, and your diagnosis. Do not change `lefthook.yml`, `.golangci.yml`, `mise.toml`, or lint configs to work around it.
- Pre-commit runs: gitleaks, gofmt/goimports, `go mod tidy -diff`, golangci-lint, `go test -short`, and for `web/`: prettier, oxlint, `tsc`, vitest. Pre-push runs the race detector and the e2e suite (see `mise.toml`) and the full web check. Agents do not push; the coordinator runs `mise run check` once on the merged tree.

## Verification ladder (agents)

Run the cheapest check that can fail for what you changed, and stop when it passes.

1. While coding: `mise exec -- go test -short ./internal/<pkg>/...` for the packages you touched (and `pnpm --dir web test` for web logic).
2. Before committing: just commit. The pre-commit hook runs fmt, lint, `go test -short ./...` and the web checks; don't run them by hand first.
3. Slice done: run only the e2e tests for your feature (`go test ./e2e/ -run <YourFeature>`), plus `mise run e2e` once if you touched `e2e/` helpers or `internal/{engine,api,agent,daemon,assist}`.
4. Do NOT run `go test -race`, `-count=N` loops, stress runs or `mise run check` unless the coordinator asked you to investigate a race or a flaky test. If your slice changes concurrency code (`internal/{agent,engine,api,assist,runner,store}`), run `mise run race` once before your final report.
5. A hook failure in a test you did not touch: rerun the hook once. If it fails again, report the test name and output to the coordinator and continue; don't investigate it.
6. Tests wait on events, not time: no new `time.Sleep` in tests; use the helpers in the package (barriers, injectable clocks, `?wait=` long-polls) instead.

## Parallel work

- Each parallel agent works in its own git worktree on its own branch and commits there. Never commit to another agent's worktree or to `main` directly; the coordinator merges.
- Stay inside your slice's files. If you need a change to a shared contract (`internal/spec`, store interfaces, `api/openapi.yaml`), message the coordinator instead of editing it.

## API-first

- Every capability is a JSON REST endpoint before it is a UI feature. The UI is a client of the public API and has no private endpoints.
- `api/openapi.yaml` is the contract. Change it in the same commit as the handler.
- Behavior is tested through the API (Go `httptest` against the real router, or the `hal9001` CLI, which is a thin API client), not through the UI.
- Agent behavior is tested against the scripted fake ACP agent (`cmd/fake-acp-agent`), not real LLMs, except in explicitly marked live tests.

## Tests

- Test behavior that a user or API client would notice: decisions, routing, errors, boundaries, state transitions, permission outcomes.
- No tests that only restate the code, check that a mock was called, or assert on incidental defaults.
- `go test -short` must stay fast; mark slow or live-dependency tests with `testing.Short()` skips.
