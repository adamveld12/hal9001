import { Link } from "react-router";
import { useFlowRun } from "../../api/runs";
import { Button } from "../../components/ui";
import { runBanner } from "./filters";

/** Above the list while it is filtered by a flow run: which run, a link to it and a way to drop the filter. */
export function RunFilterBanner({
  flowRun,
  onClear,
}: {
  flowRun: string;
  onClear: () => void;
}) {
  const run = useFlowRun(flowRun);
  const banner = runBanner(flowRun, run.data?.flow_run.flow_id);
  if (!banner) return null;
  return (
    <div
      role="status"
      aria-label="Run filter"
      className="flex items-center gap-2 border-b border-line bg-accent/10 px-3 py-2 text-xs"
    >
      <Link
        to={banner.runPath}
        className="min-w-0 flex-1 truncate font-medium text-accent underline"
      >
        {banner.text}
      </Link>
      <Button variant="ghost" onClick={onClear}>
        Clear filter
      </Button>
    </div>
  );
}
