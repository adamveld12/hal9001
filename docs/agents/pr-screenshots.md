# PR screenshots (UI changes)

Show UI changes in the PR with screenshots taken from a sandbox daemon. Never use the real daemon.

1. **Sandbox.** Start `mise run sandbox -- --port <free port other than 9001> --script <script.json> --keep` (temp home, `fake` runtime). It prints the URL and home path. Use `hal9001-sandbox`'s `--path <dir>` to restrict the PATH it uses to find the `claude`/`codex`/`omp`/`node`/`npm`/`tailscale` CLIs, and point it at stub executables you wrote in a temp dir.
2. **Fake data only.** Seed only invented data. No real names, hosts, tokens, repo contents or paths from `~`.
3. **Capture.** Chromium, viewport only (no full-page captures). Take each state in light and dark (`prefers-color-scheme`). Prefer DOM assertions to confirm the state is on screen before capturing.
4. **Save** PNGs under `docs/images/<feature>/<name>-light.png` and `<name>-dark.png`. Keep them small.
5. **Commit and push** the images, then link them in the PR body pinned to the pushed commit so the links never change:
   `https://github.com/adamveld12/hal9001/blob/<sha>/docs/images/<feature>/<file>?raw=true`
   Embed with `![alt](url)`. `<sha>` is the full SHA from `git rev-parse HEAD` after pushing.
6. **Clean up.** Stop the sandbox (Ctrl-C deletes the home unless `--keep`; delete the kept home yourself, only the path it printed), remove stub dirs and scripts you created, and leave no stray files in the worktree.
