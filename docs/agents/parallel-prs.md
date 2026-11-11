# Parallel and stacked PRs

Briefing template for a coordinator spawning agents that each build one PR.

## Template

```
Repo: adamveld12/hal9001. Read AGENTS.md first.
Slice: <what this PR does; files it may touch; files it must not touch>
Contract: <openapi.yaml operationIds + schemas this slice implements or consumes>
Worktree: `mise run wt <name>` -> ../hal9001-wt-<name>, branch feat/<name>. cd there first; absolute paths for every edit.
Base: main (or <base branch> if stacked)
Migrations: next number from the base branch: <NNNN>
Rules: signed commits, hooks never bypassed, push with `mise run push`, open the PR as DRAFT, mark ready only after `mise run checks` passes and you verified the behavior.
Final report: the last message contains the complete report (PR URL, SHA, what changed, checks run, open issues). Never "see my previous message".
```

## Rules

- **Contract first.** Land or agree on `api/openapi.yaml` (operationIds and schemas) before slices start. Slices consume the contract; changes go through the coordinator.
- **Generated types.** Never hand-resolve conflicts in `web/src/api/schema.d.ts`. Run `pnpm --dir web run gen:api` and commit the result.
- **Worktrees and branches.** One worktree per agent: `../hal9001-wt-<name>`, branch `feat/<name>`. Never edit the main checkout; `git -C <main checkout> status --short` must be empty before each commit.
- **Migrations.** Take the next number from the base branch (not from your own branch) and check again before marking ready.
- **Commits.** Signed. Never `commit.gpgsign=false`. On a signing failure retry up to 3 times with a short wait, then stop and report the exact error.
- **Draft until verified.** Open PRs as drafts; mark ready after checks pass.

## Stacked PRs

A stacked PR targets the base PR's branch. After the base PR is squash-merged into `main`:

1. Compare trees: `git diff origin/<old-base-branch> origin/main`.
   - If the diff is **empty**, the squash holds exactly your base's content, and you may `git merge -s ours origin/main`. Put the empty-diff proof in the commit message.
2. If it is **not empty**, never use `-s ours`. Either `git rebase --onto origin/main <old base tip>` or a normal `git merge origin/main` with conflicts resolved by hand.
3. Regenerate `schema.d.ts` (`pnpm --dir web run gen:api`), check migration numbers against `main`, and retarget the PR to `main`.
