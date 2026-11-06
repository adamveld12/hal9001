package e2e

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

type bulkResult struct {
	Matched            int      `json:"matched"`
	Changed            int      `json:"changed"`
	SkippedStarred     int      `json:"skipped_starred"`
	SkippedNotArchived int      `json:"skipped_not_archived"`
	ChangedIDs         []string `json:"changed_ids"`
}

func (d *Daemon) bulk(body map[string]any) bulkResult {
	d.t.Helper()
	var r bulkResult
	d.Post("/api/inbox/bulk", body).Want(200).JSON(&r)
	return r
}

// bulkInboxDaemon starts a daemon whose flows produce inbox items:
// `low` (3 items, priority low), `high` (2 items, high) and `dry` (1 item of a
// dry-run flow). Titles are "<flow> <n>".
func bulkInboxDaemon(t *testing.T) *Daemon {
	d := newDaemon(t)
	for _, f := range []struct{ id, prio, dry string }{{"low", "low", "false"}, {"high", "high", "false"}, {"dry", "normal", "true"}} {
		d.IdleSensor(f.id)
		d.Flow(f.id, fmt.Sprintf("id: %s\nenabled: true\ndry_run: %s\nsubscribe: [%s]\nnodes:\n  - {id: note, type: inbox, title: \"%s {{.input.n}}\", priority: %s}\n", f.id, f.dry, f.id, f.id, f.prio))
	}
	d.Start()
	for sensor, n := range map[string]int{"low": 3, "high": 2, "dry": 1} {
		for i := 1; i <= n; i++ {
			d.WaitRun(d.InjectRun(sensor, map[string]any{"id": fmt.Sprintf("%s-%d", sensor, i), "n": i}), "succeeded")
		}
	}
	return d
}

func TestInboxBulkIDsAndFilter(t *testing.T) {
	d := bulkInboxDaemon(t)
	items := d.InboxByTitle()
	if len(items) != 6 {
		t.Fatalf("inbox: %d items, want 6", len(items))
	}
	ids := func(titles ...string) []string {
		var out []string
		for _, ti := range titles {
			out = append(out, items[ti].ID)
		}
		return out
	}

	// ids: only the named items change; ids that do not exist are ignored.
	r := d.bulk(map[string]any{"action": "mark_read", "ids": append(ids("low 1", "high 1"), "01JZZZZZZZZZZZZZZZZZZZZZZZ")})
	if r.Matched != 2 || r.Changed != 2 {
		t.Errorf("mark_read by ids: %+v", r)
	}
	// Repeating changes nothing.
	if r = d.bulk(map[string]any{"action": "mark_read", "ids": ids("low 1")}); r.Matched != 1 || r.Changed != 0 {
		t.Errorf("repeated mark_read: %+v", r)
	}

	// filter: max_priority selects only low items; unread low items are 2.
	r = d.bulk(map[string]any{"action": "archive", "filter": map[string]any{"max_priority": "low", "read": false}})
	if r.Matched != 2 || r.Changed != 2 || len(r.ChangedIDs) != 2 {
		t.Errorf("archive low unread: %+v", r)
	}
	// older_than: nothing is a day old; everything is older than a timestamp in the future.
	if r = d.bulk(map[string]any{"action": "archive", "dry_run": true, "filter": map[string]any{"older_than": "1d"}}); r.Matched != 0 {
		t.Errorf("older_than 1d: %+v", r)
	}
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	if r = d.bulk(map[string]any{"action": "archive", "dry_run": true, "filter": map[string]any{"older_than": future}}); r.Matched != 4 {
		t.Errorf("older_than future: %+v", r)
	}
	// dry_run filter + flow_id + q.
	if r = d.bulk(map[string]any{"action": "archive", "dry_run": true, "filter": map[string]any{"dry_run": true}}); r.Matched != 1 {
		t.Errorf("filter dry_run: %+v", r)
	}
	if r = d.bulk(map[string]any{"action": "archive", "dry_run": true, "filter": map[string]any{"flow_id": "high", "q": "HIGH 2"}}); r.Matched != 1 {
		t.Errorf("filter flow_id+q: %+v", r)
	}

	// Unarchive defaults to archived items and restores them.
	if r = d.bulk(map[string]any{"action": "unarchive", "ids": r.ChangedIDs}); r.Changed != 0 {
		t.Errorf("unarchive of a dry-run id list that was never archived: %+v", r)
	}
	if r = d.bulk(map[string]any{"action": "unarchive", "filter": map[string]any{}}); r.Matched != 2 || r.Changed != 2 {
		t.Errorf("unarchive all archived: %+v", r)
	}

	// Exactly one of ids and filter; bad values are 422.
	d.Post("/api/inbox/bulk", map[string]any{"action": "archive"}).Want(422)
	d.Post("/api/inbox/bulk", map[string]any{"action": "archive", "ids": ids("low 1"), "filter": map[string]any{}}).Want(422)
	d.Post("/api/inbox/bulk", map[string]any{"action": "archive", "ids": []string{}}).Want(422)
	d.Post("/api/inbox/bulk", map[string]any{"action": "explode", "filter": map[string]any{}}).Want(422)
	d.Post("/api/inbox/bulk", map[string]any{"action": "archive", "filter": map[string]any{"older_than": "soon"}}).Want(422)
	d.Post("/api/inbox/bulk", map[string]any{"action": "archive", "filter": map[string]any{"priority": "high", "max_priority": "low"}}).Want(422)
}

func TestInboxBulkStarredProtection(t *testing.T) {
	d := bulkInboxDaemon(t)
	items := d.InboxByTitle()
	star := items["low 1"].ID
	d.Patch("/api/inbox/"+star, map[string]any{"starred": true}).Want(200)

	// A filter archive skips the starred item and says so.
	r := d.bulk(map[string]any{"action": "archive", "filter": map[string]any{"max_priority": "low"}})
	if r.Matched != 3 || r.Changed != 2 || r.SkippedStarred != 1 {
		t.Errorf("archive low: %+v", r)
	}
	if it := d.InboxByTitle()["low 1"]; it.Archived || !it.Starred {
		t.Errorf("starred item: %+v", it)
	}
	// So does an explicit id list.
	if r = d.bulk(map[string]any{"action": "archive", "ids": []string{star}}); r.Changed != 0 || r.SkippedStarred != 1 {
		t.Errorf("archive starred by id: %+v", r)
	}
	// include_starred archives it.
	if r = d.bulk(map[string]any{"action": "archive", "ids": []string{star}, "include_starred": true}); r.Changed != 1 || r.SkippedStarred != 0 {
		t.Errorf("archive starred with include_starred: %+v", r)
	}
	// Delete keeps archived starred items unless include_starred.
	if r = d.bulk(map[string]any{"action": "delete", "filter": map[string]any{}}); r.Matched != 3 || r.Changed != 2 || r.SkippedStarred != 1 {
		t.Errorf("delete archived: %+v", r)
	}
	if r = d.bulk(map[string]any{"action": "delete", "ids": []string{star}, "include_starred": true}); r.Changed != 1 {
		t.Errorf("delete starred with include_starred: %+v", r)
	}
	d.Get("/api/inbox/" + star).Want(404)
	// mark_read ignores stars.
	if r = d.bulk(map[string]any{"action": "mark_read", "filter": map[string]any{"flow_id": "high"}}); r.Changed != 2 {
		t.Errorf("mark_read: %+v", r)
	}
}

func TestInboxBulkDeleteOnlyArchived(t *testing.T) {
	d := bulkInboxDaemon(t)
	items := d.InboxByTitle()
	low1, low2, high1 := items["low 1"].ID, items["low 2"].ID, items["high 1"].ID
	d.bulk(map[string]any{"action": "archive", "ids": []string{low1}})

	r := d.bulk(map[string]any{"action": "delete", "ids": []string{low1, low2, high1}})
	if r.Matched != 3 || r.Changed != 1 || r.SkippedNotArchived != 2 || len(r.ChangedIDs) != 1 || r.ChangedIDs[0] != low1 {
		t.Errorf("delete: %+v", r)
	}
	d.Get("/api/inbox/" + low1).Want(404)
	d.Get("/api/inbox/" + low2).Want(200)
	if n := len(d.Inbox()); n != 5 {
		t.Errorf("inbox after delete: %d items, want 5", n)
	}
	// The run that produced a deleted item is untouched.
	d.Get("/api/flow-runs/" + items["low 1"].FlowRunID).Want(200)
}

func TestInboxBulkDryCountMatches(t *testing.T) {
	d := bulkInboxDaemon(t)
	d.Patch("/api/inbox/"+d.InboxByTitle()["low 1"].ID, map[string]any{"starred": true}).Want(200)
	filter := map[string]any{"max_priority": "low"}

	before := d.Inbox()
	dry := d.bulk(map[string]any{"action": "archive", "dry_run": true, "filter": filter})
	if len(d.Inbox()) != len(before) {
		t.Fatal("a dry run changed the inbox")
	}
	real := d.bulk(map[string]any{"action": "archive", "filter": filter})
	if !slices.Equal(dry.ChangedIDs, real.ChangedIDs) {
		t.Errorf("dry ids %v, real ids %v", dry.ChangedIDs, real.ChangedIDs)
	}
	dry.ChangedIDs, real.ChangedIDs = nil, nil
	if dry.Matched != real.Matched || dry.Changed != real.Changed || dry.SkippedStarred != real.SkippedStarred || dry.Changed != 2 || dry.SkippedStarred != 1 {
		t.Errorf("dry count %+v, real result %+v", dry, real)
	}
}

func TestInboxBulkUnreadCountInStatus(t *testing.T) {
	d := bulkInboxDaemon(t)
	var st Status
	d.Get("/api/status").Want(200).JSON(&st)
	// 5 unread live items; the dry-run item does not count.
	if st.UnreadInbox != 5 {
		t.Fatalf("unread_inbox = %d, want 5", st.UnreadInbox)
	}

	stream := d.secOpenStream()
	r := d.bulk(map[string]any{"action": "archive", "filter": map[string]any{"max_priority": "low"}})
	d.Get("/api/status").Want(200).JSON(&st)
	if r.Changed != 3 || st.UnreadInbox != 2 {
		t.Errorf("after archive: %+v, unread_inbox = %d, want 2", r, st.UnreadInbox)
	}
	d.Eventually("status.changed to carry unread_inbox 2", func() bool {
		return strings.Contains(stream.text(), `"unread_inbox":2`)
	})
	d.bulk(map[string]any{"action": "mark_read", "filter": map[string]any{}})
	d.Eventually("status.changed to carry unread_inbox 0", func() bool {
		return strings.Contains(stream.text(), `"unread_inbox":0`)
	})
	stream.stop()
	d.bulk(map[string]any{"action": "unarchive", "ids": r.ChangedIDs})
	d.Get("/api/status").Want(200).JSON(&st)
	// The archived items were still unread, so undoing the archive brings them back.
	if st.UnreadInbox != 3 {
		t.Errorf("after undo: unread_inbox = %d, want 3", st.UnreadInbox)
	}
}

func TestInboxBulkCLI(t *testing.T) {
	d := bulkInboxDaemon(t)
	d.InstallCLI()
	run := func(args ...string) bulkResult {
		t.Helper()
		var r bulkResult
		out, errOut, code := d.CLI("", args...)
		if code != 0 {
			t.Fatalf("hal9001 %s: exit %d\n%s\n%s", strings.Join(args, " "), code, out, errOut)
		}
		if err := json.Unmarshal([]byte(out), &r); err != nil {
			t.Fatalf("hal9001 %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return r
	}
	if r := run("inbox", "archive", "--max-priority", "low", "--preview"); r.Changed != 3 {
		t.Errorf("preview: %+v", r)
	}
	if n := len(d.Inbox()); n != 6 {
		t.Errorf("preview changed the inbox: %d", n)
	}
	if r := run("inbox", "archive", "--max-priority", "low", "--yes"); r.Changed != 3 {
		t.Errorf("archive: %+v", r)
	}
	if r := run("inbox", "delete", "--all", "--yes"); r.Changed != 3 {
		t.Errorf("delete --all: %+v", r)
	}
	if _, _, code := d.CLI("", "inbox", "archive"); code == 0 {
		t.Error("archive without a selection succeeded")
	}
	if r := run("inbox", "read", "--all"); r.Changed != 3 {
		t.Errorf("read --all: %+v", r)
	}
}
