package store

import "embed"

//go:embed migrations/*.sql
var EmbeddedMigrations embed.FS
