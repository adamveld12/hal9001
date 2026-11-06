import { useEffect, useRef, useState } from "react";
import { useValidateTool, type Tool } from "../../api/tools";
import { CodeEditor } from "../../components/CodeEditor";
import { Tabs } from "../../components/Tabs";
import { DiagnosticsList } from "../files/DiagnosticsList";
import { hasErrors } from "../files/diagnostics";
import { useDebounced } from "../files/useEditState";
import { ToolForm } from "./ToolForm";
import { emptyTool, toolToYaml } from "./toolModel";

type View = "form" | "yaml";

/**
 * The definition of a tool as a form and as YAML over one text. Form edits
 * rewrite the text; text changes from elsewhere (the YAML tab, the assistant)
 * are parsed by the server and replace the form's tool.
 */
export function ToolDefinition({
  text,
  setText,
  idLocked,
  label,
  markedLines,
}: {
  text: string;
  setText: (t: string) => void;
  idLocked: boolean;
  label: string;
  markedLines?: readonly number[];
}) {
  const [view, setView] = useState<View>("form");
  const [tool, setTool] = useState<Tool | null>(null);
  const emitted = useRef<string | null>(null);
  const validate = useValidateTool();
  const settled = useDebounced(text, 300);
  const { mutate } = validate;

  useEffect(() => {
    if (settled === emitted.current) return;
    mutate(settled, {
      onSuccess: (r) => {
        if (r.tool) setTool(r.tool);
      },
    });
  }, [settled, mutate]);

  const fromForm = (t: Tool) => {
    const y = toolToYaml(t);
    emitted.current = y;
    setTool(t);
    setText(y);
  };

  const checked = validate.data;
  const stale = checked === undefined || settled !== text;
  return (
    <div className="space-y-3">
      <Tabs
        tabs={[
          { key: "form", label: "Form" },
          { key: "yaml", label: "YAML" },
        ]}
        active={view}
        onChange={setView}
      />
      {view === "form" ? (
        tool ? (
          <ToolForm tool={tool} idLocked={idLocked} onChange={fromForm} />
        ) : (
          <div className="space-y-2 text-xs text-muted">
            {validate.isPending || validate.isIdle
              ? "Reading the definition…"
              : "The YAML does not parse yet. Fix it in the YAML tab, or start over."}
            {validate.isSuccess && (
              <div>
                <button
                  type="button"
                  className="text-accent underline"
                  onClick={() => fromForm(emptyTool())}
                >
                  Start from an empty tool
                </button>
              </div>
            )}
          </div>
        )
      ) : (
        <CodeEditor
          lang="yaml"
          label={label}
          value={text}
          onChange={setText}
          invalid={
            !stale && checked !== undefined && hasErrors(checked.diagnostics)
          }
          markedLines={markedLines}
          minHeight="14rem"
          maxHeight="36rem"
        />
      )}
      <DiagnosticsList
        diagnostics={checked?.diagnostics ?? []}
        source={stale ? undefined : text}
        stale={stale}
        emptyText={!stale ? "Valid: no problems found." : undefined}
      />
    </div>
  );
}
