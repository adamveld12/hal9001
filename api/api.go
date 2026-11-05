// Package api embeds the OpenAPI contract (openapi.yaml). Use hal9001/internal/apispec to load and query it.
package api

import _ "embed"

// OpenAPI is the raw bytes of openapi.yaml, the REST contract of the daemon (REQUIREMENTS §10.2).
//
//go:embed openapi.yaml
var OpenAPI []byte
