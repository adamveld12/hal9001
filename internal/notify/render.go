package notify

import (
	"regexp"
	"strings"

	"hal9001/internal/store"
)

// Limits of the rendered text. A notification is a glance, not a document.
const (
	maxBody     = 200
	maxSubtitle = 120
	bodyLines   = 2
	dryRunTag   = "[dry run] "
)

// Note renders the notification of an inbox item raised by flow flowID. url is the item's deep link.
func Note(flowID string, it *store.InboxItem, url string) store.Notification {
	return build(store.NotifyNote, flowID, it.Title, it.SummaryMD, string(it.Priority), it.DryRun, url, func(n *store.Notification) {
		n.ItemID = it.ID
	})
}

// ApprovalNote is what the notification of an approval is built from.
type ApprovalNote struct {
	FlowID, ApprovalID string
	Type               store.ApprovalType
	Title, Description string
	Priority           store.Priority
	DryRun, Editable   bool
}

// Approval renders the notification of an approval awaiting a decision: the title is the subtitle, the description
// the body. An empty priority is normal. The kind is the approval's type.
func Approval(a ApprovalNote, url string) store.Notification {
	prio := string(a.Priority)
	if prio == "" {
		prio = string(store.PriorityNormal)
	}
	kind := string(a.Type)
	return build(kind, a.FlowID, a.Title, a.Description, prio, a.DryRun, url, func(n *store.Notification) {
		n.ApprovalID, n.ApprovalType, n.Editable = a.ApprovalID, kind, a.Editable
	})
}

// MCPFlowID is the flow id of the notifications an external MCP client causes (they belong to no flow).
const MCPFlowID = "mcp"

// FromMCP marks a notification as raised for an external MCP client: the title names the client (or "MCP client")
// and notifications of one thread replace each other (the thread is "mcp" when the client gave none).
func FromMCP(n store.Notification, client, thread string) store.Notification {
	n.Title = "MCP client"
	if client != "" {
		n.Title = client + " (MCP)"
	}
	n.ThreadID = MCPFlowID
	if thread != "" {
		n.ThreadID += ":" + thread
	}
	return n
}

// DryRunNotice is the first line of the body of a dry run's request approval.
const DryRunNotice = "This is a dry run: approving or denying only exercises the flow's branches."

func build(kind, flowID, subtitle, bodyMD, priority string, dryRun bool, url string, set func(*store.Notification)) store.Notification {
	subtitle = oneLine(subtitle)
	if dryRun {
		subtitle = dryRunTag + subtitle
	}
	n := store.Notification{
		Kind: kind, FlowID: flowID, Title: flowID, Subtitle: truncate(subtitle, maxSubtitle),
		Body: Body(bodyMD), Priority: priority, DryRun: dryRun, ThreadID: flowID, URL: url,
	}
	set(&n)
	return n
}

// Body is the first one or two non-empty lines of a markdown text with the markup removed, at most 200 characters.
func Body(md string) string {
	var lines []string
	inFence := false
	for _, raw := range strings.Split(md, "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			inFence = !inFence
			continue
		}
		if !inFence {
			line = stripMarkdown(line)
		}
		if line == "" {
			continue
		}
		if lines = append(lines, line); len(lines) == bodyLines {
			break
		}
	}
	return truncate(strings.Join(lines, "\n"), maxBody)
}

var (
	reImage    = regexp.MustCompile(`!\[([^\]]*)\]\([^)]*\)`)
	reLink     = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	reHeading  = regexp.MustCompile(`^#{1,6}\s+`)
	reQuote    = regexp.MustCompile(`^>+\s?`)
	reBullet   = regexp.MustCompile(`^([-*+]|\d+[.)])\s+`)
	reRule     = regexp.MustCompile(`^([-*_]\s*){3,}$`)
	reEmphasis = regexp.MustCompile("(\\*\\*|__|~~|\\*|`)")
	reTag      = regexp.MustCompile(`</?[a-zA-Z][^>]*>`)
)

func stripMarkdown(line string) string {
	if reRule.MatchString(line) {
		return ""
	}
	line = reHeading.ReplaceAllString(line, "")
	line = reQuote.ReplaceAllString(line, "")
	line = reBullet.ReplaceAllString(line, "")
	line = reImage.ReplaceAllString(line, "$1")
	line = reLink.ReplaceAllString(line, "$1")
	line = reTag.ReplaceAllString(line, "")
	line = reEmphasis.ReplaceAllString(line, "")
	return strings.TrimSpace(line)
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimRight(string(r[:n-1]), " ") + "…"
}
