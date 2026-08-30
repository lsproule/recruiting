// Package migrations embeds the goose SQL migrations so `migrate` and the
// integration tests apply them without a checkout of db/.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
