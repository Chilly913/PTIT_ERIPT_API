// Package migrations embeds the golang-migrate SQL files
// (NNNNNN_name.up.sql / NNNNNN_name.down.sql) into the binaries.
package migrations

import "embed"

// FS holds every *.sql file in this directory.
//
//go:embed *.sql
var FS embed.FS
