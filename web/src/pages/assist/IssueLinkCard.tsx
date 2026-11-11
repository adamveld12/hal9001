import { CopyButton } from "../../components/CopyButton";
import { Icon } from "../../components/icons";
import type { IssueLink } from "./issueLink";

/** The result of github_issue_link: open the prefilled new-issue page in a new tab, or copy the body. */
export function IssueLinkCard({ link }: { link: IssueLink }) {
  return (
    <div
      role="group"
      aria-label="GitHub issue ready"
      className="space-y-2 rounded-md border border-accent/40 bg-accent/5 p-2 text-xs"
    >
      <div className="min-w-0">
        <div className="truncate font-medium">{link.title}</div>
        <div className="text-muted">
          New issue in <span className="font-mono">{link.repo}</span>. Nothing
          is created until you submit it on GitHub.
        </div>
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <a
          href={link.url}
          target="_blank"
          rel="noopener noreferrer"
          className="inline-flex h-7 items-center gap-1.5 whitespace-nowrap rounded-md border border-accent bg-accent px-2.5 text-xs font-medium text-accent-fg hover:opacity-90"
        >
          <Icon name="external" size={14} />
          Open issue on GitHub
        </a>
        <CopyButton text={link.body} label="Copy body" />
      </div>
      {link.truncated && (
        <div className="text-warn">
          The link holds only the start of the body (GitHub limits the length of
          a link). Open it, then use Copy body and paste the full text.
        </div>
      )}
    </div>
  );
}
