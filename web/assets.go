// Package web holds the web UI's frontend assets: server-rendered
// html/template pages and static CSS/JS, embedded into the sen binary.
// No build step (docs/design.md "Web UI"); internal/web serves them.
package web

import "embed"

// FS holds templates/*.html and static/*.
//
//go:embed templates static
var FS embed.FS
