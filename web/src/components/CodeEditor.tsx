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
import CodeMirror, {
  Decoration,
  RangeSetBuilder,
  StateEffect,
  StateField,
  type DecorationSet,
} from "@uiw/react-codemirror";
import { useEffect, useMemo, useRef } from "react";
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
  ".cm-ai-line": {
    backgroundColor: "color-mix(in srgb, var(--ok) 16%, transparent)",
    boxShadow: "inset 3px 0 0 var(--ok)",
  },
  "&.cm-focused .cm-selectionBackground, .cm-selectionBackground, ::selection":
    {
      backgroundColor: "color-mix(in srgb, var(--accent) 28%, transparent)",
    },
});

// Lines the AI changed: set with `markedLines`, kept on their text while the user edits.
const setMarks = StateEffect.define<readonly number[]>();
const markedLine = Decoration.line({ class: "cm-ai-line" });
const marksField = StateField.define<DecorationSet>({
  create: () => Decoration.none,
  update(marks, tr) {
    let next = marks.map(tr.changes);
    for (const e of tr.effects) {
      if (!e.is(setMarks)) continue;
      const b = new RangeSetBuilder<Decoration>();
      const doc = tr.state.doc;
      for (const n of e.value.toSorted((x, y) => x - y)) {
        if (n >= 1 && n <= doc.lines)
          b.add(doc.line(n).from, doc.line(n).from, markedLine);
      }
      next = b.finish();
    }
    return next;
  },
  provide: (f) => EditorView.decorations.from(f),
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
  markedLines,
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
  /** 1-based lines to highlight as changed by the AI. Pass a stable array: a new one replaces the marks. */
  markedLines?: readonly number[];
}) {
  const extensions = useMemo(
    () => [...languageExtension(lang), appTheme, highlight, marksField],
    [lang],
  );
  const view = useRef<EditorView | null>(null);
  useEffect(() => {
    view.current?.dispatch({ effects: setMarks.of(markedLines ?? []) });
  }, [markedLines]);
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
        onCreateEditor={(v) => {
          view.current = v;
        }}
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
