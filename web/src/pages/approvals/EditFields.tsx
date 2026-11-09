import { useState } from "react";
import { CodeEditor } from "../../components/CodeEditor";
import { Field, TextArea } from "../../components/ui";
import { buildEdits, editorKind, initialDrafts, type Editable } from "./edits";

/** One editable box per path: a textarea for strings, a JSON editor for anything else. */
export function EditFields({
  editable,
  drafts,
  onChange,
}: {
  editable: readonly Editable[];
  drafts: Readonly<Record<string, string>>;
  onChange: (key: string, text: string) => void;
}) {
  const { errors } = buildEdits(editable, drafts);
  return (
    <div className="space-y-3">
      {editable.map((e) => {
        const text = drafts[e.key] ?? "";
        const error = errors[e.key];
        return (
          <Field
            key={e.key}
            label={`Edit ${e.key}`}
            error={error ? [`Not valid JSON: ${error}`] : []}
          >
            {editorKind(e.value) === "text" ? (
              <TextArea
                rows={Math.min(12, Math.max(3, text.split("\n").length + 1))}
                value={text}
                onChange={(ev) => onChange(e.key, ev.target.value)}
              />
            ) : (
              <CodeEditor
                label={`Edit ${e.key}`}
                lang="json"
                value={text}
                invalid={error !== undefined}
                onChange={(v) => onChange(e.key, v)}
                minHeight="4rem"
                maxHeight="16rem"
              />
            )}
          </Field>
        );
      })}
    </div>
  );
}

/** The edit boxes with their own state, for the inspector's preview (nothing is sent). */
export function EditFieldsPreview({
  editable,
}: {
  editable: readonly Editable[];
}) {
  const [drafts, setDrafts] = useState(() => initialDrafts(editable));
  return (
    <EditFields
      editable={editable}
      drafts={{ ...initialDrafts(editable), ...drafts }}
      onChange={(k, t) => setDrafts((d) => ({ ...d, [k]: t }))}
    />
  );
}
