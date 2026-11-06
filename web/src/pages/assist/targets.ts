// What differs between the kinds of thing the assistant edits. The drawer, the
// session lookup and the draft sync take these from here instead of branching
// on the kind themselves. Pure data and functions; the panels that need React
// (test, save) are chosen by kind in AssistDrawer.tsx.

import type { AssistSession, AssistTargetKind } from "../../api/assist";

export interface AssistTargetConfig {
  kind: AssistTargetKind;
  /** Lower-case singular, for sentences: "the draft of the flow". */
  noun: string;
  /** Header of the start view for a target that has no file yet. */
  newTitle: string;
  /** Header of the start view for an existing target. */
  existingTitle: (id: string) => string;
  /** Header of an open session. `id` is null while the target has no file. */
  sessionTitle: (id: string | null) => string;
  /** One paragraph under the start view's header. */
  intro: string;
  composerPlaceholder: { new: string; existing: string };
  starterPrompts: { new: readonly string[]; existing: readonly string[] };
  /** Page that edits the target `id` with the session open. */
  editorPath: (id: string, sessionId: string) => string;
  /** Page that shows the session before its target has a file. */
  draftPath: (sessionId: string) => string;
}

const SENSOR: AssistTargetConfig = {
  kind: "sensor",
  noun: "sensor",
  newTitle: "Create a sensor with AI",
  existingTitle: (id) => `Ask AI about ${id}`,
  sessionTitle: (id) => (id === null ? "New sensor" : `Sensor ${id}`),
  intro:
    "The agent edits a draft of the sensor, which appears in the editor. Nothing is saved until you press Save. It never sees your secrets; it tests the draft through the daemon.",
  composerPlaceholder: {
    new: "Describe the sensor you want…",
    existing: "What should change?",
  },
  starterPrompts: {
    new: [
      "Poll my GitHub review requests",
      "Watch a Linear project for new issues",
      "Check an RSS feed for new posts",
      "Poll a JSON API I describe",
    ],
    existing: [
      "Explain what this sensor does",
      "Make the dedup key more stable",
      "Add error handling to the script",
      "Reduce the polling frequency",
    ],
  },
  editorPath: (id, sessionId) => `/sensors/${id}?assist=${sessionId}`,
  draftPath: (sessionId) => `/sensors?assist=${sessionId}`,
};

const FLOW: AssistTargetConfig = {
  kind: "flow",
  noun: "flow",
  newTitle: "Create a flow with AI",
  existingTitle: (id) => `Ask AI about ${id}`,
  sessionTitle: (id) => (id === null ? "New flow" : `Flow ${id}`),
  intro:
    "The agent edits a draft of the flow, which appears on the canvas. Nothing is saved until you press Save, and the agent never enables a flow. It never sees your secrets; it tests the draft through the daemon, and agent nodes are not run in a test.",
  composerPlaceholder: {
    new: "Describe the flow you want…",
    existing: "What should change?",
  },
  starterPrompts: {
    new: [
      "Triage my GitHub review requests",
      "Send urgent Slack mentions to my inbox",
      "Summarize new email",
    ],
    existing: [
      "Explain what this flow does",
      "Add an inbox node for urgent items",
      "Drop events that are not about me",
    ],
  },
  editorPath: (id, sessionId) => `/flows/${id}?assist=${sessionId}`,
  draftPath: (sessionId) => `/flows?assist=${sessionId}`,
};

const TOOL: AssistTargetConfig = {
  kind: "tool",
  noun: "tool",
  newTitle: "Create a tool with AI",
  existingTitle: (id) => `Ask AI about ${id}`,
  sessionTitle: (id) => (id === null ? "New tool" : `Tool ${id}`),
  intro:
    "The agent edits a draft of the tool. Nothing is saved until you press Save. It never sees your secrets; it tests the draft through the daemon.",
  composerPlaceholder: {
    new: "Describe the tool you want…",
    existing: "What should change?",
  },
  starterPrompts: {
    new: [
      "A tool that fetches a URL and returns JSON",
      "A tool that runs a git command",
    ],
    existing: ["Explain what this tool does", "Add a parameter"],
  },
  editorPath: (id, sessionId) => `/tools/${id}?assist=${sessionId}`,
  draftPath: (sessionId) => `/tools?assist=${sessionId}`,
};

const TARGETS: Record<AssistTargetKind, AssistTargetConfig> = {
  sensor: SENSOR,
  flow: FLOW,
  tool: TOOL,
};

export function targetConfig(kind: AssistTargetKind): AssistTargetConfig {
  return TARGETS[kind];
}

/** Suggested first messages for a new target (`targetId` null) or an existing one. */
export function starterPrompts(
  kind: AssistTargetKind,
  targetId: string | null,
): readonly string[] {
  const p = TARGETS[kind].starterPrompts;
  return targetId === null ? p.new : p.existing;
}

/**
 * Sessions of the same target that can be picked up again, newest first.
 * `targetId` null is a new target that has no file yet.
 */
export function selectResumable(
  sessions: readonly AssistSession[],
  kind: AssistTargetKind,
  targetId: string | null,
): AssistSession[] {
  return sessions
    .filter(
      (s) =>
        s.status !== "closed" &&
        s.target.kind === kind &&
        s.target.id === targetId,
    )
    .toSorted((a, b) =>
      a.updated_at < b.updated_at ? 1 : a.updated_at > b.updated_at ? -1 : 0,
    );
}
