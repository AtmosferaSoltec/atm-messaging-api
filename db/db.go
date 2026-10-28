// Package db embeds the SQL migrations in the binary, so the production image
// does not need the source tree to apply them.
package db

import "embed"

// Migrations holds the files under db/migrations.
//
//go:embed migrations
var Migrations embed.FS
