package menubar

import (
	"net/url"
	"strconv"
)

// Kind classifies a menu entry.
type Kind int

const (
	Link      Kind = iota // opens URL in the browser
	Info                  // disabled label
	Separator             // divider
	Submenu               // has Children
	Quit                  // quits the app
)

// Entry is one menu item.
type Entry struct {
	Kind     Kind
	Title    string
	URL      string // Link only
	Children []Entry
}

// Menu is the full render state of the menubar item.
type Menu struct {
	Reachable bool
	Title     string // text next to the icon; empty hides it
	Entries   []Entry
}

// InboxCount matches the web header badge: unread notes plus pending approvals.
func InboxCount(s Status) int { return s.UnreadInbox + s.PendingApprovals }

// BuildMenu turns a snapshot (nil when the daemon is unreachable) into menu entries. ui is the UI base URL.
func BuildMenu(snap *Snapshot, ui string) Menu {
	link := func(title, path string) Entry { return Entry{Kind: Link, Title: title, URL: ui + path} }
	m := Menu{Reachable: snap != nil}
	if snap == nil {
		m.Entries = append(m.Entries, Entry{Kind: Info, Title: "hal9001 is not running"}, Entry{Kind: Separator})
	} else if n := InboxCount(snap.Status); n > 0 {
		m.Title = strconv.Itoa(n)
	}

	flows := Entry{Kind: Submenu, Title: "Flows"}
	sensors := Entry{Kind: Submenu, Title: "Sensors"}
	if snap != nil {
		for _, f := range snap.Flows {
			if f.Enabled != nil && *f.Enabled {
				flows.Children = append(flows.Children, link(f.ID, "/flows/"+url.PathEscape(f.ID)))
			}
		}
		for _, s := range snap.Sensors {
			t := s.ID
			if s.Status.Paused {
				t += " (paused)"
			}
			sensors.Children = append(sensors.Children, link(t, "/sensors/"+url.PathEscape(s.ID)))
		}
	}
	flows.Children = append(flows.Children, link("All flows", "/flows"))
	sensors.Children = append(sensors.Children, link("All sensors", "/sensors"))

	m.Entries = append(m.Entries,
		link("Inbox", "/inbox"),
		flows,
		sensors,
		link("Settings", "/settings/general"),
		Entry{Kind: Separator},
		Entry{Kind: Quit, Title: "Quit"},
	)
	return m
}
