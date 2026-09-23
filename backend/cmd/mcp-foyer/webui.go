package main

import (
	"io/fs"

	"mcp-foyer/internal/webui"
)

// webUI returns the built frontend, or nil when this binary was built
// without one. See package webui for how that is decided.
func webUI() fs.FS { return webui.FS() }
