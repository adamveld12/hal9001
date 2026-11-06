package api

import (
	"net/http"
	"slices"
	"time"

	"hal9001/internal/spec"
	"hal9001/internal/store"
)

// inboxBulkFilter is the filter of a bulk request: the fields of GET /api/inbox
// (except paging and sort) plus max_priority and older_than.
type inboxBulkFilter struct {
	Read        *bool   `json:"read"`
	Starred     *bool   `json:"starred"`
	Archived    *string `json:"archived"`
	Priority    *string `json:"priority"`
	MaxPriority *string `json:"max_priority"`
	DryRun      *bool   `json:"dry_run"`
	FlowID      *string `json:"flow_id"`
	OlderThan   *string `json:"older_than"`
	Q           *string `json:"q"`
}

// priorityRange lists the priorities from min to max (either may be empty).
func priorityRange(minP, maxP string) []store.Priority {
	lo, hi := 0, len(priorities)-1
	if minP != "" {
		lo = slices.Index(priorities, minP)
	}
	if maxP != "" {
		hi = slices.Index(priorities, maxP)
	}
	var out []store.Priority
	if lo > hi {
		return nil
	}
	for _, p := range priorities[lo : hi+1] {
		out = append(out, store.Priority(p))
	}
	return out
}

// storeFilter validates f. Unset archived means unarchived items, except for
// unarchive and delete, which default to archived items.
func (f *inboxBulkFilter) storeFilter(action string, now time.Time) (*store.InboxFilter, string, string) {
	out := &store.InboxFilter{Read: f.Read, Starred: f.Starred, DryRun: f.DryRun}
	archived := "false"
	if action == store.InboxUnarchive || action == store.InboxDelete {
		archived = "true"
	}
	if f.Archived != nil {
		archived = *f.Archived
	}
	switch archived {
	case "true":
		out.Archived = new(true)
	case "false":
		out.Archived = new(false)
	case "any":
	default:
		return nil, "filter.archived", "archived must be true, false or any"
	}
	var minP, maxP string
	for _, p := range []struct {
		src  *string
		dst  *string
		name string
	}{{f.Priority, &minP, "priority"}, {f.MaxPriority, &maxP, "max_priority"}} {
		if p.src == nil {
			continue
		}
		if !slices.Contains(priorities, *p.src) {
			return nil, "filter." + p.name, p.name + " must be one of low, normal, high, urgent"
		}
		*p.dst = *p.src
	}
	if minP != "" || maxP != "" {
		if out.Priorities = priorityRange(minP, maxP); len(out.Priorities) == 0 {
			return nil, "filter.max_priority", "max_priority is below priority"
		}
	}
	if f.FlowID != nil {
		if !spec.ValidID(*f.FlowID) {
			return nil, "filter.flow_id", "flow_id must be a flow id"
		}
		out.FlowID = *f.FlowID
	}
	if f.Q != nil {
		if *f.Q == "" {
			return nil, "filter.q", "q must not be empty"
		}
		out.Search = *f.Q
	}
	if f.OlderThan != nil {
		if d, err := spec.ParseDuration(*f.OlderThan); err == nil && d > 0 {
			out.Time.Until = now.Add(-d)
		} else if t, err := time.Parse(time.RFC3339Nano, *f.OlderThan); err == nil {
			out.Time.Until = t.UTC()
		} else {
			return nil, "filter.older_than", "older_than must be a positive duration such as 7d or an RFC 3339 time"
		}
	}
	return out, "", ""
}

func (s *Server) bulkInbox(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action         string           `json:"action"`
		IDs            []string         `json:"ids"`
		Filter         *inboxBulkFilter `json:"filter"`
		IncludeStarred bool             `json:"include_starred"`
		DryRun         bool             `json:"dry_run"`
	}
	if !decodeJSON(w, r, &req, false) {
		return
	}
	if !slices.Contains([]string{store.InboxArchive, store.InboxUnarchive, store.InboxMarkRead, store.InboxMarkUnread, store.InboxDelete}, req.Action) {
		fieldProblem(w, "action", "action must be one of archive, unarchive, mark_read, mark_unread, delete")
		return
	}
	if (req.Filter == nil) == (req.IDs == nil) {
		fieldProblem(w, "", "set exactly one of ids and filter")
		return
	}
	b := store.InboxBulk{Action: req.Action, IncludeStarred: req.IncludeStarred, DryRun: req.DryRun}
	if req.Filter != nil {
		f, path, msg := req.Filter.storeFilter(req.Action, time.Now().UTC())
		if msg != "" {
			fieldProblem(w, path, msg)
			return
		}
		b.Filter = f
	} else {
		if len(req.IDs) == 0 || len(req.IDs) > store.MaxBulkInboxIDs {
			fieldProblem(w, "ids", "ids must hold 1 to 1000 inbox item ids")
			return
		}
		for _, id := range req.IDs {
			if !ulidRE.MatchString(id) {
				fieldProblem(w, "ids", "ids must be inbox item ids (ULIDs)")
				return
			}
		}
		b.IDs = req.IDs
	}
	res, err := s.opts.Store.BulkInbox(r.Context(), b)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"matched": res.Matched, "changed": res.Changed,
		"skipped_starred": res.SkippedStarred, "skipped_not_archived": res.SkippedNotArchived,
		"changed_ids": res.ChangedIDs,
	})
}
