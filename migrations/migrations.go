// Package migrations holds the database migrations. golang-migrate applies them in version order,
// like Flyway. Each change is a pair of files with the same number and name:
//
//	000001_create_venues.up.sql    applies the change
//	000001_create_venues.down.sql  undoes it
//
// A migration that was applied anywhere is never edited; a later change gets a new pair of files.
// See docs/open/08-development.md for the commands.
package migrations

import "embed"

// Files holds every .sql file in this folder, built into the program by go:embed. The migrate
// command and the tests read the migrations from here, so they work from any working directory.
// "find every file matching *.sql in this package's directory and embed those files into the variable Files."
// command below is a compiler directive, they are special comments that communicate instructions to go compiler.

//go:embed *.sql
var Files embed.FS
