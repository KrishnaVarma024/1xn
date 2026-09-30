// Package db opens the database and runs transactions with the tenant set.
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

// Open connects to Postgres and checks the connection.
//
// Driver note: we use database/sql with lib/pq. The architecture doc names
// pgx; code written against database/sql moves to pgx by swapping the import
// for github.com/jackc/pgx/v5/stdlib (see docs/adr/0008).
func Open(ctx context.Context, dsn string) (*sql.DB, error) {
	d, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	d.SetMaxOpenConns(20)
	d.SetMaxIdleConns(10)
	d.SetConnMaxIdleTime(5 * time.Minute)
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := d.PingContext(pingCtx); err != nil {
		d.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return d, nil
}

// InTenantTx runs fn inside a transaction whose Row-Level Security context is
// tenantID. Every query fn makes can only see and write that tenant's rows.
//
// The setting is transaction-local (set_config's third argument is true), so
// it vanishes at COMMIT or ROLLBACK and can never leak to the next request
// that reuses this pooled connection.
//
// Deadlocks and serialization failures are retried up to three times; fn
// must therefore be safe to run again (no side effects outside the tx).
func InTenantTx(ctx context.Context, d *sql.DB, tenantID uuid.UUID, fn func(tx *sql.Tx) error) error {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		err = runTx(ctx, d, tenantID, fn)
		if !retryable(err) {
			return err
		}
		time.Sleep(time.Duration(10*(attempt+1)) * time.Millisecond)
	}
	return err
}

// InTenantReadTx is InTenantTx for reads that must see one consistent
// snapshot across several queries (REPEATABLE READ, read-only). Sync pull uses
// it so the cursor it returns matches the data it returns.
func InTenantReadTx(ctx context.Context, d *sql.DB, tenantID uuid.UUID, fn func(tx *sql.Tx) error) error {
	return runTxOpts(ctx, d, tenantID, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}, fn)
}

func runTx(ctx context.Context, d *sql.DB, tenantID uuid.UUID, fn func(tx *sql.Tx) error) (err error) {
	return runTxOpts(ctx, d, tenantID, nil, fn)
}

func runTxOpts(ctx context.Context, d *sql.DB, tenantID uuid.UUID, opts *sql.TxOptions, fn func(tx *sql.Tx) error) (err error) {
	tx, err := d.BeginTx(ctx, opts)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if _, err = tx.ExecContext(ctx, `SELECT set_config('app.tenant_id', $1, true)`, tenantID.String()); err != nil {
		return err
	}
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// retryable reports Postgres errors that mean "try the whole transaction
// again": 40001 serialization_failure and 40P01 deadlock_detected.
func retryable(err error) bool {
	var pe *pq.Error
	if errors.As(err, &pe) {
		return pe.Code == "40001" || pe.Code == "40P01"
	}
	return false
}

// IsUniqueViolation reports a 23505 unique_violation, optionally on a named
// constraint.
func IsUniqueViolation(err error, constraint string) bool {
	var pe *pq.Error
	if errors.As(err, &pe) && pe.Code == "23505" {
		return constraint == "" || pe.Constraint == constraint
	}
	return false
}
