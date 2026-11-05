// Test-only fixtures. Not imported by shipped code.
import type { Flow } from "./model";

/** The example flow from REQUIREMENTS §6, with positions persisted. */
export function triageGithub(): Flow {
  return {
    id: "triage-github",
    enabled: true,
    dry_run: false,
    subscribe: ["github-notifications"],
    max_concurrent: 2,
    nodes: [
      {
        id: "only-review-requests",
        type: "command",
        lang: "jq",
        script:
          'select(.reason == "review_requested") | . + {_reason: "review requested"}',
        ui: { x: 100, y: 80 },
      },
      {
        id: "classify",
        type: "command",
        lang: "python",
        ports: ["small", "large"],
        script: "import json, sys\n",
        ui: { x: 400, y: 80 },
      },
      {
        id: "review-small",
        type: "agent",
        runtime: "claude",
        mode: "approve",
        approve: "proposal",
        prompt: "pr-review",
        extra_prompt: "Focus on tests.",
        model: "sonnet",
        cwd: {
          mode: "worktree",
          repo: "~/code/{{.input.repository.name}}",
          ref: "origin/main",
        },
        timeout: "20m",
        ui: { x: 700, y: 0 },
      },
      {
        id: "note-large",
        type: "inbox",
        title: "Large PR needs you: {{.input.subject.title}}",
        priority: "high",
        summary_md: "{{.input.subject.url}}",
        ui: { x: 700, y: 160 },
      },
    ],
    edges: [
      { from: "only-review-requests", to: "classify" },
      { from: "classify", port: "small", to: "review-small" },
      { from: "classify", port: "large", to: "note-large" },
    ],
  };
}
