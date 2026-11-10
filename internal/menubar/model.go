package menubar

import (
	"net/url"
	"strconv"
	"strings"
)

// Kind classifies a menu entry.
type Kind int

const (
	Link      Kind = iota // opens URL in the browser
	Info                  // disabled label
	Separator             // divider
	Submenu               // has Children
	Quit                  // quits the app
	Action                // runs the app's handler for ID
)

// Action IDs of the entries that act on the service and the app itself.
const (
	ActionServiceStart   = "service.start"
	ActionServiceStop    = "service.stop"
	ActionServiceRestart = "service.restart"
	ActionOpenLogs       = "logs.open"
	ActionLaunchAtLogin  = "login.toggle"
	ActionApproveLogin   = "login.approve" // opens System Settings > Login Items
	ActionInstallCLI     = "cli.install"
	ActionRemoveLegacy   = "legacy.remove"
	// ActionNotificationSettings opens the app's pane in System Settings > Notifications.
	ActionNotificationSettings = "notifications.settings"
	// ActionUpdateInstall asks the daemon to install the available update; ActionUpdateCheck to look for one now.
	ActionUpdateInstall = "update.install"
	ActionUpdateCheck   = "update.check"
)

// Updater states (UpdateStatus.State) the menu tells apart.
const (
	stateInstalling = "installing"
	stateError      = "error"
	// phaseRequested is the install phase before the app reported anything: it says nothing worth showing.
	phaseRequested = "requested"
)

// maxUpdateErrorLen is how much of a failed update's message fits in a menu line; the log has all of it.
const maxUpdateErrorLen = 80

// Entry is one menu item.
type Entry struct {
	Kind     Kind
	Title    string
	URL      string // Link only
	ID       string // Action only
	Checked  bool   // Action only: shows a check mark
	Disabled bool   // Action only: shown greyed out
	Children []Entry
}

// Menu is the full render state of the menubar item.
type Menu struct {
	Reachable bool
	Title     string // text next to the icon; empty hides it
	Entries   []Entry
}

// ServiceState is what the app knows about the daemon service it manages; it does not come from the daemon's API.
type ServiceState struct {
	// Bundled is false when the app does not run from an app bundle (go run); there is no service to manage then.
	Bundled bool
	// Managed reports that the app owns the daemon's launchd agent: bundled, no headless or older agent in the way.
	Managed bool
	// Registered reports that the bundled agent is registered (started at login, kept alive).
	Registered bool
	// NeedsApproval reports that registration waits for approval in System Settings > Login Items.
	NeedsApproval bool
	// LaunchAtLogin reports that the app itself starts at login.
	LaunchAtLogin bool
	// Headless reports a headless service (`hal9001 service install`); the app then leaves the daemon alone.
	Headless bool
	// Legacy reports agents written by older versions (`hal9001 install`, `hal9001-menubar install`).
	Legacy bool
	// AppVersion is the version of the app, shown while the daemon does not answer.
	AppVersion string
	// Home is the daemon home shown while the daemon does not answer; UserHome abbreviates it to ~.
	Home, UserHome string
	// NotificationsOff reports that the user turned this app's notifications off; terminal-notifier shows them instead.
	NotificationsOff bool
	// AlertStyleHint suggests the Alerts style, once: an urgent notification came as a banner that disappears by itself.
	AlertStyleHint bool
	// UpdateInstalling reports that this app is carrying out an update install right now; UpdatePhase says how far it
	// got ("downloading 40%"), empty before the first step.
	UpdateInstalling bool
	UpdatePhase      string
	// ServiceUpdating reports that the app is registering the service again after it replaced itself: launchd refuses
	// the new build's daemon for a while, so the daemon does not answer for up to a couple of minutes.
	ServiceUpdating bool
}

// InboxCount matches the web header badge: unread notes plus pending approvals.
func InboxCount(s Status) int { return s.UnreadInbox + s.PendingApprovals }

// StatusLine is the first line of the menu: whether the daemon answers, and its version.
func StatusLine(snap *Snapshot, svc ServiceState) string {
	switch {
	case snap != nil:
		v := snap.Version.Version
		if v == "" {
			v = svc.AppVersion
		}
		return withVersion("Running", v)
	case svc.ServiceUpdating:
		return "Updating service… (up to 2 min)"
	case svc.Registered:
		return withVersion("Not responding", svc.AppVersion)
	}
	return withVersion("Stopped", svc.AppVersion)
}

func withVersion(state, version string) string {
	if version == "" {
		return state
	}
	return state + " · " + version
}

func homeLine(snap *Snapshot, svc ServiceState) string {
	home := svc.Home
	if snap != nil && snap.Version.Home != "" {
		home = snap.Version.Home
	}
	if home == "" {
		return ""
	}
	if svc.UserHome != "" && (home == svc.UserHome || strings.HasPrefix(home, svc.UserHome+"/")) {
		home = "~" + strings.TrimPrefix(home, svc.UserHome)
	}
	return "Home: " + home
}

// BuildMenu turns a snapshot (nil when the daemon is unreachable) into menu entries. ui is the UI base URL.
func BuildMenu(snap *Snapshot, ui string, svc ServiceState) Menu {
	link := func(title, path string) Entry { return Entry{Kind: Link, Title: title, URL: ui + path} }
	m := Menu{Reachable: snap != nil}
	var upd *UpdateStatus
	if snap != nil {
		upd = snap.Update
		if n := InboxCount(snap.Status); n > 0 {
			m.Title = strconv.Itoa(n)
		}
	}
	m.Entries = append(m.Entries, Entry{Kind: Info, Title: StatusLine(snap, svc)})
	if h := homeLine(snap, svc); h != "" {
		m.Entries = append(m.Entries, Entry{Kind: Info, Title: h})
	}
	if svc.Bundled && svc.NotificationsOff {
		m.Entries = append(m.Entries, Entry{Kind: Action, ID: ActionNotificationSettings, Title: "Notifications are off — Open Settings"})
	}
	if svc.Bundled && svc.AlertStyleHint {
		m.Entries = append(m.Entries, Entry{Kind: Action, ID: ActionNotificationSettings, Title: "Urgent notifications vanish as banners — switch to Alerts…"})
	}
	if svc.Bundled {
		m.Entries = append(m.Entries, updateEntries(upd, svc)...)
	}
	m.Entries = append(m.Entries, Entry{Kind: Separator})

	var inbox, approvals int
	if snap != nil {
		inbox, approvals = snap.Status.UnreadInbox, snap.Status.PendingApprovals
	}
	flows := Entry{Kind: Submenu, Title: "Flows", Children: []Entry{link("All flows", "/flows")}}
	sensors := Entry{Kind: Submenu, Title: "Sensors", Children: []Entry{link("All sensors", "/sensors")}}
	if snap != nil {
		for _, f := range snap.Flows {
			if f.Enabled != nil && *f.Enabled {
				flows.Children = append(flows.Children, link(counted(f.ID, f.Health.Running), "/flows/"+url.PathEscape(f.ID)))
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
	for _, sub := range []*Entry{&flows, &sensors} {
		if len(sub.Children) > 1 {
			sub.Children = append(sub.Children[:1], append([]Entry{{Kind: Separator}}, sub.Children[1:]...)...)
		}
	}

	m.Entries = append(m.Entries,
		link(counted("Inbox", inbox), "/inbox"),
		link(counted("Approvals", approvals), "/inbox?kind=action"),
		flows,
		sensors,
		link("Settings", "/settings/general"),
		Entry{Kind: Separator},
	)
	if svc.Bundled {
		m.Entries = append(m.Entries, serviceEntries(svc, upd)...)
		m.Entries = append(m.Entries, Entry{Kind: Separator})
	}
	m.Entries = append(m.Entries, Entry{Kind: Quit, Title: "Quit"})
	return m
}

// updateEntries is the part of the menu about app updates, below the status lines. upd is nil when the daemon reports
// nothing (an older daemon, or it does not answer); the entries then reflect only what this app is doing itself.
func updateEntries(upd *UpdateStatus, svc ServiceState) []Entry {
	info := func(title string) []Entry { return []Entry{{Kind: Info, Title: title}} }
	version := upd.latestVersion()
	switch {
	case svc.UpdateInstalling || upd != nil && upd.Enabled && upd.State == stateInstalling:
		return info(installingLine(upd, svc))
	case upd == nil || !upd.Enabled:
		return nil
	case upd.Pending:
		if version == "" {
			return info("Update installs when flows finish")
		}
		return info("Update " + version + " installs when flows finish")
	case upd.Available:
		var es []Entry
		if upd.State == stateError && upd.Error != "" {
			es = append(es, Entry{Kind: Info, Title: "Update failed: " + shorten(upd.Error)})
		}
		title := "Update available"
		if version != "" {
			title += ": " + version
		}
		return append(es, Entry{Kind: Action, ID: ActionUpdateInstall, Title: title + " — Install & Restart"})
	}
	return nil
}

// installingLine says an install is running, with how far it is when that is known.
func installingLine(upd *UpdateStatus, svc ServiceState) string {
	phase := svc.UpdatePhase
	if phase == "" && upd != nil && upd.Phase != phaseRequested {
		phase = upd.Phase
	}
	if phase == "" {
		return "Installing update…"
	}
	return "Installing update… (" + phase + ")"
}

// latestVersion is the version of the newest release the daemon knows, or "".
func (u *UpdateStatus) latestVersion() string {
	if u == nil || u.Latest == nil {
		return ""
	}
	return u.Latest.Version
}

// shorten collapses whitespace and cuts s to maxUpdateErrorLen characters, marking the cut.
func shorten(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > maxUpdateErrorLen {
		return string(r[:maxUpdateErrorLen-1]) + "…"
	}
	return s
}

// serviceEntries is the part of the menu about the daemon service and the app's own settings.
func serviceEntries(svc ServiceState, upd *UpdateStatus) []Entry {
	var es []Entry
	action := func(id, title string) Entry { return Entry{Kind: Action, ID: id, Title: title} }
	if svc.Headless {
		es = append(es,
			Entry{Kind: Info, Title: "A headless service runs the daemon"},
			Entry{Kind: Info, Title: "Remove it with: hal9001 service uninstall"})
	}
	if svc.Legacy {
		es = append(es, action(ActionRemoveLegacy, "Remove older launch agents…"))
	}
	if svc.Managed {
		if svc.NeedsApproval {
			es = append(es, action(ActionApproveLogin, "Allow in System Settings…"))
		}
		stop := action(ActionServiceStop, "Stop service")
		stop.Disabled = !svc.Registered
		restart := action(ActionServiceRestart, "Restart service")
		restart.Disabled = !svc.Registered
		start := action(ActionServiceStart, "Start service")
		start.Disabled = svc.Registered
		es = append(es, Entry{Kind: Submenu, Title: "Service", Children: []Entry{start, stop, restart}})
	}
	es = append(es, action(ActionOpenLogs, "Open logs"))
	if upd != nil && upd.Enabled {
		es = append(es, action(ActionUpdateCheck, "Check for updates"))
	}
	login := action(ActionLaunchAtLogin, "Launch at login")
	login.Checked = svc.LaunchAtLogin
	return append(es, login, action(ActionInstallCLI, "Install command-line tool…"))
}

func counted(label string, n int) string {
	if n > 0 {
		return label + " (" + strconv.Itoa(n) + ")"
	}
	return label
}
