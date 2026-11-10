package notify

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"

	"hal9001/internal/store"
)

// Limits of the rendered text. A notification is a glance, not a document.
const (
	maxBody     = 200
	maxSubtitle = 120
	maxArgs     = 60
	bodyLines   = 2
	dryRunTag   = "[dry run] "
)

// Note renders the notification of an inbox item raised by flow flowID. url is the item's deep link.
func Note(flowID string, it *store.InboxItem, url string) store.Notification {
	return build(store.NotifyNote, flowID, it.Title, it.SummaryMD, string(it.Priority), it.DryRun, url, func(n *store.Notification) {
		n.ItemID = it.ID
	})
}

// Proposal renders the notification of an agent's proposal awaiting approval.
func Proposal(flowID, approvalID, proposal string, dryRun bool, url string) store.Notification {
	return build(store.NotifyProposal, flowID, "Approval: "+firstLine(proposal), proposal, string(store.PriorityNormal), dryRun, url, func(n *store.Notification) {
		n.ApprovalID, n.ApprovalType = approvalID, store.NotifyProposal
	})
}

// ToolCall renders the notification of a tool call awaiting approval. tool is the tool's title (or kind) and
// rawInput its JSON arguments, shown shortened.
func ToolCall(flowID, approvalID, tool string, rawInput json.RawMessage, dryRun bool, url string) store.Notification {
	subtitle := "Tool call: " + strings.TrimSpace(tool)
	if args := shortArgs(rawInput); args != "" {
		subtitle += " " + args
	}
	return build(store.NotifyToolCall, flowID, subtitle, "", string(store.PriorityHigh), dryRun, url, func(n *store.Notification) {
		n.ApprovalID, n.ApprovalType = approvalID, store.NotifyToolCall
	})
}

// Request renders the notification of an approval node's request. req.Title is the rendered title.
func Request(flowID, approvalID string, req store.RequestApproval, url string) store.Notification {
	prio := string(req.Priority)
	if prio == "" {
		prio = string(store.PriorityNormal)
	}
	body := strings.TrimSpace(strings.TrimPrefix(req.BodyMD, DryRunNotice))
	return build(store.NotifyRequest, flowID, "Request: "+req.Title, body, prio, req.DryRun, url, func(n *store.Notification) {
		n.ApprovalID, n.ApprovalType, n.Editable = approvalID, store.NotifyRequest, len(req.Editable) > 0
	})
}

// DryRunNotice is the first line of the body of a dry run's request approval; the renderer drops it from the body.
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

// shortArgs renders a tool call's arguments on one line: a JSON object as key=value pairs, anything else as is.
func shortArgs(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil || len(obj) == 0 {
		return truncate(oneLine(string(raw)), maxArgs)
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		v := obj[k]
		s, ok := v.(string)
		if !ok {
			b, _ := json.Marshal(v)
			s = string(b)
		}
		parts = append(parts, k+"="+oneLine(s))
	}
	return truncate(strings.Join(parts, " "), maxArgs)
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return stripMarkdown(s)
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimRight(string(r[:n-1]), " ") + "…"
}
