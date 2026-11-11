import { Markdown } from "../../components/Markdown";
import { Badge } from "../../components/ui";

/** What an approval node asks, as the flow inspector previews it: a dry-run notice, the body, and the fields table. */
export function RequestBody({
  request,
}: {
  request: {
    dry_run: boolean;
    body_md: string;
    fields: readonly { key: string; value: string }[];
  };
}) {
  return (
    <div className="space-y-3">
      {request.dry_run && <Badge tone="warn">DRY RUN</Badge>}
      {request.body_md ? (
        <Markdown>{request.body_md}</Markdown>
      ) : (
        <p className="m-0 text-xs text-muted">No details.</p>
      )}
      {request.fields.length > 0 && (
        <table className="w-full border-collapse text-xs">
          <tbody>
            {request.fields.map((f) => (
              <tr key={f.key} className="border-t border-line">
                <th className="w-1/3 py-1 pr-3 text-left font-normal text-muted">
                  {f.key}
                </th>
                <td className="break-words py-1">{f.value}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}
