package mcpclient

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// resultOf converts a tool result: the text blocks joined by newlines, other content described in brackets, and the
// structured content when there is one.
func resultOf(r *mcp.CallToolResult) CallResult {
	out := CallResult{IsError: r.IsError}
	var parts []string
	for _, c := range r.Content {
		switch c := c.(type) {
		case *mcp.TextContent:
			parts = append(parts, c.Text)
		case *mcp.ImageContent:
			parts = append(parts, fmt.Sprintf("[image %s, %d bytes]", c.MIMEType, len(c.Data)))
		case *mcp.AudioContent:
			parts = append(parts, fmt.Sprintf("[audio %s, %d bytes]", c.MIMEType, len(c.Data)))
		case *mcp.ResourceLink:
			parts = append(parts, "[resource "+c.URI+"]")
		case *mcp.EmbeddedResource:
			switch {
			case c.Resource == nil:
			case c.Resource.Text != "":
				parts = append(parts, c.Resource.Text)
			default:
				parts = append(parts, fmt.Sprintf("[resource %s %s, %d bytes]", c.Resource.URI, c.Resource.MIMEType, len(c.Resource.Blob)))
			}
		}
	}
	out.Text = strings.Join(parts, "\n")
	if r.StructuredContent != nil {
		if b, err := json.Marshal(r.StructuredContent); err == nil && string(b) != "null" {
			out.Structured = b
		}
	}
	return out
}
