import ReactMarkdown from "react-markdown";
import { cx } from "../lib/cx";

/** Renders trusted-or-not markdown without raw HTML (react-markdown drops it). */
export function Markdown({
  children,
  className,
}: {
  children: string;
  className?: string;
}) {
  return (
    <div
      className={cx(
        "text-[13px] leading-relaxed [&_a]:text-accent [&_a]:underline [&_code]:rounded [&_code]:bg-raised [&_code]:px-1 [&_code]:font-mono [&_code]:text-[12px]",
        "[&_h1]:mb-2 [&_h1]:text-base [&_h1]:font-semibold [&_h2]:mb-2 [&_h2]:mt-3 [&_h2]:text-sm [&_h2]:font-semibold [&_h3]:mt-3 [&_h3]:font-semibold",
        "[&_li]:ml-4 [&_ol]:list-decimal [&_p]:my-2 [&_pre]:overflow-x-auto [&_pre]:rounded-md [&_pre]:bg-raised [&_pre]:p-2 [&_pre_code]:bg-transparent [&_ul]:list-disc",
        "[&_blockquote]:border-l-2 [&_blockquote]:border-line [&_blockquote]:pl-3 [&_blockquote]:text-muted",
        className,
      )}
    >
      <ReactMarkdown>{children}</ReactMarkdown>
    </div>
  );
}
