package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"strings"
)

// inboxBulkLeaf builds `inbox archive|read|delete`. With one <id> and no other
// selection it keeps the single-item PATCH; with --ids, --all or a filter flag
// it calls POST /api/inbox/bulk. Filter-based actions ask for confirmation on a
// terminal, showing the count from a dry run.
//
// field is the PATCH field of the single-item form ("" for delete, which has
// none); offFlag names the flag that reverses the action.
func inboxBulkLeaf(verb, action, offAction, field, offFlag, offHelp string) *leaf {
	var (
		off, all, yes, preview, includeStarred bool
		ids                                    string
		state, priority, maxPriority, flow, q  string
		olderThan                              string
		starred, archived, dryRunFlows         bool
	)
	return &leaf{
		usage: "[<id> | --ids <id,...> | --all | filter flags] [--include-starred] [--yes]", maxArgs: 1,
		flags: func(fs *flag.FlagSet) {
			if offFlag != "" {
				fs.BoolVar(&off, offFlag, false, offHelp)
			}
			fs.StringVar(&ids, "ids", "", "comma-separated item ids")
			fs.BoolVar(&all, "all", false, "every item the default view shows (unarchived; archived for delete and --undo)")
			fs.BoolVar(&includeStarred, "include-starred", false, "archive/delete starred items too (kept by default)")
			fs.BoolVar(&yes, "yes", false, "do not ask for confirmation")
			fs.BoolVar(&preview, "preview", false, "only count what would change")
			fs.StringVar(&state, "state", "", "filter: read or unread")
			fs.BoolVar(&starred, "starred", false, "filter: only starred items")
			fs.BoolVar(&archived, "archived", false, "filter: only archived items")
			fs.StringVar(&priority, "priority", "", "filter: minimum priority")
			fs.StringVar(&maxPriority, "max-priority", "", "filter: maximum priority (--max-priority low: only low)")
			fs.StringVar(&flow, "flow", "", "filter: flow id")
			fs.StringVar(&q, "q", "", "filter: search text")
			fs.StringVar(&olderThan, "older-than", "", "filter: created before a duration ago (7d) or an RFC 3339 time")
			fs.BoolVar(&dryRunFlows, "dry-run-flows", false, "filter: only items of dry-run flows")
		},
		run: func(ctx context.Context, c *call) error {
			filter := map[string]any{}
			set := func(k string, v any) { filter[k] = v }
			switch state {
			case "":
			case "read":
				set("read", true)
			case "unread":
				set("read", false)
			default:
				return usagef("inbox %s: --state must be read or unread", verb)
			}
			if starred {
				set("starred", true)
			}
			if archived {
				set("archived", "true")
			}
			for k, v := range map[string]string{"priority": priority, "max_priority": maxPriority, "flow_id": flow, "q": q, "older_than": olderThan} {
				if v != "" {
					set(k, v)
				}
			}
			if dryRunFlows {
				set("dry_run", true)
			}
			useFilter := all || len(filter) > 0
			var idList []string
			for _, id := range strings.Split(ids, ",") {
				if id = strings.TrimSpace(id); id != "" {
					idList = append(idList, id)
				}
			}
			idList = append(idList, c.args...)
			switch {
			case useFilter && len(idList) > 0:
				return usagef("inbox %s: use ids or --all/filter flags, not both", verb)
			case !useFilter && len(idList) == 0:
				return usagef("inbox %s: name an <id>, --ids, or --all / filter flags", verb)
			}
			act := action
			if off {
				act = offAction
			}
			if len(c.args) == 1 && ids == "" && !preview && field != "" {
				body, _ := jsonBody(map[string]bool{field: !off})
				return c.doEmit(ctx, http.MethodPatch, "/api/inbox/"+esc(c.args[0]), nil, body)
			}
			req := map[string]any{"action": act, "include_starred": includeStarred}
			if useFilter {
				req["filter"] = filter
			} else {
				req["ids"] = idList
			}
			if preview {
				req["dry_run"] = true
				return c.doEmit(ctx, http.MethodPost, "/api/inbox/bulk", nil, mustJSON(req))
			}
			if useFilter && !yes && isTerminal(c.env.Stdin) {
				req["dry_run"] = true
				resp, err := c.client().do(ctx, http.MethodPost, "/api/inbox/bulk", nil, mustJSON(req))
				if err != nil {
					return err
				}
				var n struct {
					Changed        int `json:"changed"`
					SkippedStarred int `json:"skipped_starred"`
				}
				if err := json.Unmarshal(resp.Body, &n); err != nil {
					return fmt.Errorf("inbox %s: read the count: %w", verb, err)
				}
				msg := fmt.Sprintf("%s %d inbox item(s)?", strings.ReplaceAll(act, "_", " "), n.Changed)
				if n.SkippedStarred > 0 {
					msg += fmt.Sprintf(" %d starred item(s) are kept.", n.SkippedStarred)
				}
				if act == "delete" {
					msg += " Deleting is permanent."
				}
				_, _ = fmt.Fprintf(c.stderr(), "%s [y/N] ", msg)
				line, _ := bufio.NewReader(c.env.Stdin).ReadString('\n')
				if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
					return usagef("inbox %s: cancelled", verb)
				}
				delete(req, "dry_run")
			}
			return c.doEmit(ctx, http.MethodPost, "/api/inbox/bulk", nil, mustJSON(req))
		},
	}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
