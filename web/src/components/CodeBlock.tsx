import { CopyButton } from "./CopyButton";

/** A monospace block with a copy button. */
export function CodeBlock({ text, label }: { text: string; label?: string }) {
  return (
    <div className="flex items-start gap-2 rounded-md border border-line bg-raised p-2">
      <pre className="m-0 min-w-0 flex-1 overflow-auto whitespace-pre-wrap break-all font-mono text-xs">
        {text}
      </pre>
      <CopyButton text={text} label={label} />
    </div>
  );
}
