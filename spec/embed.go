// Package specfs embeds the spec files shipped with nebu.
package specfs

import (
	"embed"
	"io/fs"
)

//go:embed probes formats archs runtimes triage recipes all:patches
var files embed.FS

// Returns embedded spec filesystem
func FS() fs.FS { return files }
