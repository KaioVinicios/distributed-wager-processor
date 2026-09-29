// Package migrations embeds the versioned SQL migrations (data-model.md §7) so
// that the tests apply exactly the files the migrate service applies.
package migrations

import "embed"

// FS holds the NNNNNN_name.{up,down}.sql files, read by golang-migrate's iofs source.
//
//go:embed *.sql
var FS embed.FS
