// Package docs embeds the user-facing format documentation the daemon hands to AI assist sessions.
package docs

import _ "embed"

// Flows is docs/flows.md: the sensor, flow, node and prompt formats.
//
//go:embed flows.md
var Flows string
