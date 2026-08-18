// Package db embeds the migration files so the shipped binary can migrate itself.
//
// ADR-0006: an officer double-clicking tod-serve.exe has no migration CLI on their PATH, so goose
// is a library the binary embeds rather than a tool the deployment has to provide. Atlas authors
// what is in here; nothing at runtime needs Atlas.
package db

import (
	"embed"
	"fmt"
	"io/fs"
)

// migrationsDir is the embedded directory. Named once so the embed directive, the fs.Sub below
// and any error message cannot disagree about which directory this is.
const migrationsDir = "migrations-sqlite"

//go:embed migrations-sqlite/*.sql
var migrations embed.FS

// Migrations returns the migration files, rooted so that goose sees `000001_initial_schema.sql`
// rather than `migrations-sqlite/000001_initial_schema.sql` — goose parses the version out of the
// name, and a path prefix would make every version unparseable.
func Migrations() (fs.FS, error) {
	sub, err := fs.Sub(migrations, migrationsDir)
	if err != nil {
		return nil, fmt.Errorf("open embedded %s: %w", migrationsDir, err)
	}
	return sub, nil
}
