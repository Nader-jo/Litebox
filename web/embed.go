// Package webassets embeds the complete dependency-free browser surface.
package webassets

import "embed"

// Static contains CSS, JavaScript, icons, and vendored HTMX.
//
//go:embed static/*
var Static embed.FS
