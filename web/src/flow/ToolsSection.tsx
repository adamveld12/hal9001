import { useTools } from "../api/tools";
import { Field, Select } from "../components/ui";
import {
  BUILTIN_TOOLS,
  approveChoice,
  isSelected,
  setApproveChoice,
  toggleTool,
  unknownTools,
  type ApproveChoice,
  type NodeToolRef,
} from "./toolSelection";

/** Tools multi-select of an agent node: custom tools with an approval override each, built-ins read-only. */
export function ToolsSection({
  value,
  error,
  onChange,
}: {
  value: NodeToolRef[] | undefined;
  error?: string[];
  onChange: (next: NodeToolRef[] | undefined) => void;
}) {
  const tools = useTools();
  const available = (tools.data?.items ?? [])
    .filter((t) => t.tool !== null)
    .map((t) => t.id);
  const missing = unknownTools(value, available);
  return (
    <Field
      label="Tools"
      error={error}
      hint={
        <>
          Always available: {BUILTIN_TOOLS.join(", ")}.
          {available.length === 0 && " No custom tools defined yet."}
        </>
      }
    >
      <div className="space-y-1" data-testid="node-tools">
        {[...available, ...missing].map((id) => {
          const on = isSelected(value, id);
          const choice = approveChoice(value, id);
          return (
            <div key={id} className="flex items-center gap-2 text-xs">
              <label className="flex flex-1 items-center gap-2">
                <input
                  type="checkbox"
                  checked={on}
                  onChange={(e) =>
                    onChange(toggleTool(value, id, e.target.checked))
                  }
                />
                <span className={missing.includes(id) ? "text-bad" : undefined}>
                  {id}
                  {missing.includes(id) && " (no such tool)"}
                </span>
              </label>
              {on && (
                <Select
                  aria-label={`Approval for ${id}`}
                  value={choice}
                  onChange={(e) =>
                    onChange(
                      setApproveChoice(
                        value,
                        id,
                        e.target.value as ApproveChoice,
                      ),
                    )
                  }
                >
                  <option value="default">tool default</option>
                  <option value="always">always ask</option>
                  <option value="never">never ask</option>
                  {choice === "custom" && (
                    <option value="custom">conditional</option>
                  )}
                </Select>
              )}
            </div>
          );
        })}
      </div>
    </Field>
  );
}
