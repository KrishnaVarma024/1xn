package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

// Migrate applies every *.sql file in fsys that has not been applied yet, in
// name order, each in its own transaction. It must run as the owner role.
//
// Safety rails:
//   - a Postgres advisory lock makes two deploys that start at the same time
//     take turns instead of racing;
//   - each applied file's SHA-256 is stored; if someone edits a file that was
//     already applied, Migrate refuses to continue instead of silently
//     leaving environments with different schemas.
func Migrate(ctx context.Context, d *sql.DB, fsys fs.FS) ([]string, error) {
	conn, err := d.Conn(ctx) // advisory locks belong to one session
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	const lockKey = 7_104_222_026 // any constant; "1x.n migrations"
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, lockKey); err != nil {
		return nil, fmt.Errorf("take migration lock: %w", err)
	}
	defer conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, lockKey)

	if _, err := conn.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migration (
			version    text PRIMARY KEY,
			checksum   text NOT NULL,
			applied_at timestamptz NOT NULL DEFAULT now()
		)`); err != nil {
		return nil, err
	}

	applied := map[string]string{}
	rows, err := conn.QueryContext(ctx, `SELECT version, checksum FROM schema_migration`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var v, c string
		if err := rows.Scan(&v, &c); err != nil {
			rows.Close()
			return nil, err
		}
		applied[v] = c
	}
	rows.Close()

	names, err := fs.Glob(fsys, "*.sql")
	if err != nil {
		return nil, err
	}
	sort.Strings(names)

	var ran []string
	for _, name := range names {
		body, err := fs.ReadFile(fsys, name)
		if err != nil {
			return ran, err
		}
		sum := sha256.Sum256(body)
		checksum := hex.EncodeToString(sum[:])
		version := strings.TrimSuffix(name, ".sql")

		if prev, ok := applied[version]; ok {
			if prev != checksum {
				return ran, fmt.Errorf("migration %s was edited after it was applied; add a new migration instead", name)
			}
			continue
		}

		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return ran, err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback()
			return ran, fmt.Errorf("apply %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migration (version, checksum) VALUES ($1, $2)`, version, checksum); err != nil {
			tx.Rollback()
			return ran, err
		}
		if err := tx.Commit(); err != nil {
			return ran, fmt.Errorf("commit %s: %w", name, err)
		}
		ran = append(ran, name)
	}
	return ran, nil
}
