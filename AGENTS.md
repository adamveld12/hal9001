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
- Pre-commit first fixes and re-stages your staged files (gofmt/goimports on Go, prettier on `web/`, and regenerates `web/src/api/schema.d.ts` from `api/openapi.yaml`), so formatting never fails a commit; unstaged changes are left alone. It then checks: gitleaks, `go mod tidy -diff`, golangci-lint, `go test -short`, and for `web/`: oxlint, `tsc`, vitest. Pre-push runs the sharded e2e suite and the web check. The race detector is opt-in (`mise run race`).

## Never touch the user's machine state (agents)

- Never run app or DMG builds, mounts or installs (`mise run app`, `release`, `dmg`, `dmg:check`, `app:run`), and never launch the menubar/app.
- Never touch the user's installed HAL9001.app, `~/Applications`, `/Applications`, `~/.hal9001`, `~/Library` (LaunchAgents, logs, Application Support) or their running service.
- App and DMG builds and checks run in CI (`.github/workflows/release.yml`). `scripts/build-app.sh` installs nothing unless `HAL9001_APP_INSTALL_DIR` is set; that is for the user.
- Test the daemon with the e2e harness or `mise run sandbox` (temp homes, non-9001 ports).

## Verification (agents): keep it light

This is a fun side project; iteration speed beats rigor.

1. While coding: run the tests of the packages you touched, once.
2. Commit: the pre-commit hook does formatting, lint, `go test -short` and web checks. Don't run them by hand first.
3. Add a test only for the behavior you built. Run your own new e2e test once if you wrote one.
4. Don't run `-race`, `-count=N` loops, stress runs, `mise run check`, full e2e or browser verification passes unless asked.
5. A hook failure in a test you didn't touch: rerun once; if it fails again, mention it in your report and move on.

## Worktrees only (agents)

- Work in `../hal9001-wt-<name>`; create it with `mise run wt <name>`, `cd` there first, and use absolute paths under it for every edit. Never edit the main checkout.
- Before each commit, `git -C <main checkout> status --short` must be empty.

## Never without explicit user permission

- Commands that change real external systems: `tailscale` (except `version`, `status --json`, `serve status --json`), `gh release`, `launchctl`, `sfltool`, a real `npm install -g`.
- Deletes that use globs. Only delete paths you created.
- Anything that disables hooks: `core.hooksPath`, `--no-verify`, `LEFTHOOK=0`.

## Signed commits and pushing

- Commits are signed. Never set `commit.gpgsign=false`. If signing fails (for example 1Password: "failed to fill whole buffer"), wait a few seconds and retry the same commit, up to 3 times, then stop and report the exact error.
- Push with `mise run push` (uses `gh` credentials; see `scripts/push.sh`). `mise run checks` runs every pre-commit check over the whole tree without writing files.

## Final report

- The last message of a task contains the complete report. Never write "see my previous message".

## Flaky tests

If a pre-push test fails in code you didn't touch, retry once; if it fails again, report it. Don't debug unrelated failures.

## Parallel work

- Each parallel agent works in its own git worktree on its own branch and commits there. Never commit to another agent's worktree or to `main` directly; the coordinator merges.
- Stay inside your slice's files. If you need a change to a shared contract (`internal/spec`, store interfaces, `api/openapi.yaml`), message the coordinator instead of editing it.
- `schema.d.ts` is generated and has a keep-ours merge rule (`.gitattributes`; the driver is set by `mise run setup`). After merging, run `mise exec -- pnpm --dir web gen:api` and commit the result.

- Parallel and stacked PRs (contract first, migration numbering, squash-merge procedure, briefing template): skill `hal-parallel-prs` (`.agents/skills/hal-parallel-prs/SKILL.md`).

## Browser verification

- Screenshots for UI PRs: skill `hal-pr-screenshots` (`.agents/skills/hal-pr-screenshots/SKILL.md`).

- Use the sandbox, never the daemon on port 9001: `mise run sandbox` (seeded temp home, a `fake` runtime from `--script`, a free port; prints the URL and home path, Ctrl-C stops it and deletes the home unless `--keep`). `--path <stubdir>:/usr/bin:/bin` makes that the only PATH the daemon uses to find the `claude`/`codex`/`omp` CLIs, `node` and `npm`, so runtime detection (Settings → Runtimes) can be tried with stub executables instead of the machine's real CLIs.
- Prefer `tab.text()`, `waitForSelector` and DOM assertions. Take screenshots only of the viewport, only as final evidence; never full-page captures (they stall).
- A virtual WebAuthn authenticator needs a headed tab.

## API-first

- Every capability is a JSON REST endpoint before it is a UI feature. The UI is a client of the public API and has no private endpoints.
- `api/openapi.yaml` is the contract. Change it in the same commit as the handler.
- Behavior is tested through the API (Go `httptest` against the real router, or the `hal9001` CLI, which is a thin API client), not through the UI.
- Agent behavior is tested against the scripted fake ACP agent (`cmd/fake-acp-agent`), not real LLMs, except in explicitly marked live tests.

## Tests

- Test behavior that a user or API client would notice: decisions, routing, errors, boundaries, state transitions, permission outcomes.
- No tests that only restate the code, check that a mock was called, or assert on incidental defaults.
- `go test -short` must stay fast; mark slow or live-dependency tests with `testing.Short()` skips.
