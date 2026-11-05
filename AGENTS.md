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
- Pre-commit runs: gitleaks, gofmt/goimports, `go mod tidy -diff`, golangci-lint, `go test -short`, and for `web/`: prettier, oxlint, `tsc`, vitest. Pre-push adds `go test -race` and the full web check.

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
