import { useState } from "react";
import type { RemoteMcpTool } from "../../api/mcpServers";
import { CodeBlock } from "../../components/CodeBlock";
import { TextInput } from "../../components/ui";
import { FILTER_THRESHOLD, filterTools, schemaText } from "./mcpServerView";

/** The cached tools of one server; click a tool for its input (and output) schema. */
export function McpToolList({ tools }: { tools: readonly RemoteMcpTool[] }) {
  const [query, setQuery] = useState("");
  const [open, setOpen] = useState<string | null>(null);
  const shown = filterTools(tools, query);
  return (
    <div className="space-y-2">
      {tools.length > FILTER_THRESHOLD && (
        <TextInput
          aria-label="Filter tools"
          placeholder="Filter tools"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          className="max-w-xs"
        />
      )}
      {tools.length === 0 && (
        <div className="text-xs text-muted">
          No tools cached. Refresh the tool list once the server is connected.
        </div>
      )}
      {tools.length > 0 && shown.length === 0 && (
        <div className="text-xs text-muted">No tool matches.</div>
      )}
      <ul className="m-0 list-none divide-y divide-line rounded-md border border-line p-0">
        {shown.map((t) => {
          const isOpen = open === t.name;
          return (
            <li key={t.name}>
              <button
                type="button"
                aria-expanded={isOpen}
                onClick={() => setOpen(isOpen ? null : t.name)}
                className="flex w-full items-start gap-2 px-2.5 py-1.5 text-left hover:bg-raised"
              >
                <span aria-hidden="true" className="w-3 pt-0.5 text-muted">
                  {isOpen ? "▾" : "▸"}
                </span>
                <span className="min-w-0 flex-1">
                  <span className="font-mono text-xs font-medium">
                    {t.name}
                  </span>
                  {t.title && (
                    <span className="ml-2 text-xs text-muted">{t.title}</span>
                  )}
                  {t.description && (
                    <span
                      className={
                        isOpen
                          ? "mt-0.5 block whitespace-pre-wrap text-xs text-muted"
                          : "mt-0.5 line-clamp-2 block text-xs text-muted"
                      }
                    >
                      {t.description}
                    </span>
                  )}
                </span>
              </button>
              {isOpen && (
                <div className="space-y-2 px-2.5 pb-2.5 pl-8">
                  <div className="text-[11px] font-medium text-muted">
                    Input schema
                  </div>
                  <CodeBlock
                    text={schemaText(t.input_schema)}
                    label="Copy schema"
                  />
                  {t.output_schema && (
                    <>
                      <div className="text-[11px] font-medium text-muted">
                        Output schema
                      </div>
                      <CodeBlock
                        text={schemaText(t.output_schema)}
                        label="Copy schema"
                      />
                    </>
                  )}
                </div>
              )}
            </li>
          );
        })}
      </ul>
    </div>
  );
}
