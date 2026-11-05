import { javascript } from "@codemirror/lang-javascript";
import { json } from "@codemirror/lang-json";
import { markdown } from "@codemirror/lang-markdown";
import { python } from "@codemirror/lang-python";
import { yaml } from "@codemirror/lang-yaml";
import {
  HighlightStyle,
  StreamLanguage,
  syntaxHighlighting,
} from "@codemirror/language";
import { shell } from "@codemirror/legacy-modes/mode/shell";
import { EditorView } from "@codemirror/view";
import { tags as t } from "@lezer/highlight";
import CodeMirror from "@uiw/react-codemirror";
import { useMemo } from "react";
import { cx } from "../lib/cx";

export type CodeLang =
  "yaml" | "json" | "python" | "javascript" | "shell" | "markdown" | "text";

function languageExtension(lang: CodeLang) {
  switch (lang) {
    case "yaml":
      return [yaml()];
    case "json":
      return [json()];
    case "python":
      return [python()];
    case "javascript":
      return [javascript()];
    case "shell":
      return [StreamLanguage.define(shell)];
    case "markdown":
      return [markdown()];
    case "text":
      return [];
  }
}

// Colours come from the app's CSS variables so light/dark switch without
// reconfiguring the editor.
const appTheme = EditorView.theme({
  "&": { color: "var(--fg)", backgroundColor: "var(--panel)" },
  ".cm-content": { caretColor: "var(--fg)", fontFamily: "var(--font-mono)" },
  ".cm-gutters": {
    backgroundColor: "var(--raised)",
    color: "var(--muted)",
    border: "none",
  },
  ".cm-activeLine, .cm-activeLineGutter": {
    backgroundColor: "color-mix(in srgb, var(--accent) 8%, transparent)",
  },
  "&.cm-focused .cm-selectionBackground, .cm-selectionBackground, ::selection":
    {
      backgroundColor: "color-mix(in srgb, var(--accent) 28%, transparent)",
    },
});

const highlight = syntaxHighlighting(
  HighlightStyle.define([
    {
      tag: [t.keyword, t.operatorKeyword, t.controlKeyword],
      color: "var(--run)",
    },
    { tag: [t.string, t.special(t.string)], color: "var(--ok)" },
    { tag: [t.number, t.bool, t.null], color: "var(--warn)" },
    {
      tag: [t.comment, t.lineComment, t.blockComment],
      color: "var(--muted)",
      fontStyle: "italic",
    },
    {
      tag: [
        t.propertyName,
        t.definition(t.variableName),
        t.function(t.variableName),
      ],
      color: "var(--info)",
    },
    { tag: [t.heading], color: "var(--accent)", fontWeight: "600" },
  ]),
);

export function CodeEditor({
  value,
  onChange,
  lang,
  readOnly,
  minHeight = "8rem",
  maxHeight = "28rem",
  invalid,
  label,
  className,
}: {
  value: string;
  onChange?: (v: string) => void;
  lang: CodeLang;
  readOnly?: boolean;
  minHeight?: string;
  maxHeight?: string;
  invalid?: boolean;
  label?: string;
  className?: string;
}) {
  const extensions = useMemo(
    () => [...languageExtension(lang), appTheme, highlight],
    [lang],
  );
  return (
    <div
      className={cx(
        "overflow-hidden rounded-md border",
        invalid ? "border-bad" : "border-line",
        className,
      )}
      aria-label={label}
    >
      <CodeMirror
        value={value}
        onChange={onChange}
        extensions={extensions}
        readOnly={readOnly}
        editable={!readOnly}
        minHeight={minHeight}
        maxHeight={maxHeight}
        theme="none"
        basicSetup={{
          foldGutter: false,
          highlightActiveLine: !readOnly,
          autocompletion: false,
        }}
      />
    </div>
  );
}
