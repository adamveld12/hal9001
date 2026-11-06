// Package content ships the starter configuration that hal9001 seeds into its
// home directory (~/.hal9001) on first run.
package content

import "embed"

// FS contains config.yaml, env.example, sensors/, tools/, flows/ and prompts/ at its root.
//
//go:embed config.yaml env.example sensors tools flows prompts
var FS embed.FS
