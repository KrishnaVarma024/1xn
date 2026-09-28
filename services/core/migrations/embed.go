// Package migrations holds the SQL schema, embedded into the binary so the
// service and its schema always ship together.
//
// Rules: files are applied in name order, each in its own transaction, and a
// file is never edited after it has been applied anywhere (the runner stores a
// checksum and refuses to start if one changes). To change the schema, add a
// new file.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
