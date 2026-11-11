// What the Chat page tells the assistant about itself (see lib/pageContext.ts):
// the filters, how many conversations are listed and the selected one. Ids,
// titles, counts and statuses only; never message text.

import type { Conversation } from "../../api/conversations";
import { countBy, factList, rowEntities } from "../../lib/contextSummary";
import type { PageContextInput } from "../../lib/pageContext";
import { CHAT_FILTER_KEYS, type ChatFilters } from "./filters";
import { flowNodeLabel } from "./status";

const entity = (c: Conversation) => ({
  kind: "conversation",
  id: c.id,
  label: c.title,
  detail: [c.source, flowNodeLabel(c), c.runtime, c.status.replaceAll("_", " ")]
    .filter(Boolean)
    .join(", "),
});

export function chatPageContext(p: {
  filters: ChatFilters;
  /** Conversations in the loaded pages of the list. */
  listed: readonly Conversation[];
  hasMore: boolean;
  /** The conversation open in the right pane. */
  selected: Conversation | null;
  /** The new-chat view is open. */
  starting: boolean;
}): PageContextInput {
  const { filters: f, selected: s } = p;
  return {
    title: s === null ? "Chat" : `Chat: ${s.title}`,
    summary: p.starting
      ? "The Chat page with the start view of a new chat."
      : "The Chat page: every conversation (chats and flow agent runs) on the left, one transcript on the right.",
    facts: factList([
      ...CHAT_FILTER_KEYS.map((k) => [`${k} filter`, f[k]] as const),
      ["conversations loaded", p.listed.length],
      ["by status", countBy(p.listed, (c) => c.status)],
      ["more pages", p.hasMore],
      ["selected conversation", s?.id],
      ["selected source", s?.source],
      ["selected status", s?.status],
      ["selected flow / node", s === null ? null : flowNodeLabel(s)],
      ["selected runtime", s?.runtime],
      ["continued from run", s?.forked_from?.agent_run_id],
      ["forks", s && s.fork_count > 0 ? s.fork_count : null],
    ]),
    entities: [
      ...(s === null ? [] : [entity(s)]),
      ...rowEntities(
        p.listed.filter((c) => c.id !== s?.id),
        entity,
        19,
      ),
    ],
  };
}
