// Package migrations embeds the goose SQL migration files applied by the
// storage layer on startup.
package migrations

import "embed"

// FS holds every goose migration shipped with the wzap binary.
//
//go:embed *.sql
var FS embed.FS
