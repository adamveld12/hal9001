// Jump-to-item commands: flows, sensors, tools, prompts and recent
// conversations by name, built from the lists the pages already fetch.

import type { Command } from "./types";

/** Items registered per kind; the palette shows fewer (see search.ts). */
export const ITEM_CAP = 100;

export interface ItemSources {
  flows?: readonly { id: string; description?: string }[];
  sensors?: readonly { id: string }[];
  tools?: readonly { id: string }[];
  prompts?: readonly { name: string }[];
  conversations?: readonly {
    id: string;
    title: string;
    preview: string;
    source: string;
  }[];
}

/**
 * One command per item, each opening the item's page. `conversationPath`
 * is the page of a conversation (the Chat page).
 */
export function itemCommands(
  src: ItemSources,
  go: (path: string) => void,
  conversationPath: (id: string) => string,
): Command[] {
  const out: Command[] = [];
  for (const f of (src.flows ?? []).slice(0, ITEM_CAP)) {
    out.push({
      id: `flow:${f.id}`,
      title: f.id,
      group: "Flows",
      icon: "flows",
      subtitle: "Flow",
      keywords: f.description ? [f.description] : undefined,
      queryOnly: true,
      run: () => go(`/flows/${encodeURIComponent(f.id)}`),
    });
  }
  for (const s of (src.sensors ?? []).slice(0, ITEM_CAP)) {
    out.push({
      id: `sensor:${s.id}`,
      title: s.id,
      group: "Sensors",
      icon: "sensors",
      subtitle: "Sensor",
      queryOnly: true,
      run: () => go(`/sensors/${encodeURIComponent(s.id)}`),
    });
  }
  for (const t of (src.tools ?? []).slice(0, ITEM_CAP)) {
    out.push({
      id: `tool:${t.id}`,
      title: t.id,
      group: "Tools",
      icon: "command",
      subtitle: "Tool",
      queryOnly: true,
      run: () => go(`/tools/${encodeURIComponent(t.id)}`),
    });
  }
  for (const p of (src.prompts ?? []).slice(0, ITEM_CAP)) {
    out.push({
      id: `prompt:${p.name}`,
      title: p.name,
      group: "Prompts",
      icon: "prompts",
      subtitle: "Prompt",
      queryOnly: true,
      run: () => go(`/prompts/${encodeURIComponent(p.name)}`),
    });
  }
  for (const c of (src.conversations ?? []).slice(0, ITEM_CAP)) {
    out.push({
      id: `conversation:${c.id}`,
      title: c.title,
      group: "Conversations",
      icon: "chat",
      subtitle: c.source === "chat" ? "Chat" : "Flow run",
      keywords: c.preview ? [c.preview] : undefined,
      queryOnly: true,
      run: () => go(conversationPath(c.id)),
    });
  }
  return out;
}
