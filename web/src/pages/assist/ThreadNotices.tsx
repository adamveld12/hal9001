import { Link } from "react-router";
import { shortId } from "../../lib/format";
import { splitRunRefs } from "./threadItems";

/** Text with each agent run id after "run " linked to that run's page. */
function RunText({ text }: { text: string }) {
  return (
    <>
      {splitRunRefs(text).map((p, i) =>
        p.type === "run" ? (
          <Link
            key={i}
            to={`/agent-runs/${p.id}`}
            title={p.id}
            className="text-accent hover:underline"
          >
            {shortId(p.id)}
          </Link>
        ) : (
          <span key={i}>{p.text}</span>
        ),
      )}
    </>
  );
}

/** "Continued from run X; ..." at the start of a forked chat: a centered line across the transcript. */
export function DividerLine({ text }: { text: string }) {
  return (
    <div className="flex items-center gap-2 py-1 text-[11px] text-muted">
      <span aria-hidden="true" className="h-px flex-1 bg-line" />
      <span className="max-w-[85%] text-center">
        <RunText text={text} />
      </span>
      <span aria-hidden="true" className="h-px flex-1 bg-line" />
    </div>
  );
}

/** A system notice (for example that the sandbox was recreated): muted, not an error. */
export function NoticeLine({ text }: { text: string }) {
  return (
    <div
      role="status"
      className="whitespace-pre-wrap break-words rounded-md border border-line bg-raised px-2 py-1.5 text-xs text-muted"
    >
      <RunText text={text} />
    </div>
  );
}
