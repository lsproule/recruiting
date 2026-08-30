// Package problems embeds the platform's seed problem bank, which every org
// reads and none may edit. The JSON format is documented in README.md.
package problems

import "embed"

// FS holds every seed document, one JSON array of problems per file.
//
//go:embed *.json
var FS embed.FS
