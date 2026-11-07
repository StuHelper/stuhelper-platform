// Package postgres persists platform configuration and installation facts.
package postgres

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	e "github.com/StuHelper/stuhelper-platform/eligibility"
	"time"
)

//go:embed schema.sql
var Schema string

type Repository struct{ DB *sql.DB }

func Migrate(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(726391840111)`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, Schema); err != nil {
		return err
	}
	return tx.Commit()
}
func (r *Repository) Ready(ctx context.Context) error {
	var revision int64
	if err := r.DB.QueryRowContext(ctx, `SELECT revision FROM platform.configuration WHERE singleton`).Scan(&revision); err != nil || revision < 1 {
		return e.ErrUnavailable
	}
	return nil
}
func (r *Repository) CheckRuntime(ctx context.Context) error {
	var unsafe bool
	err := r.DB.QueryRowContext(ctx, `SELECT rolsuper OR rolcreaterole OR rolcreatedb OR has_schema_privilege(current_user,'platform','CREATE') OR has_table_privilege(current_user,'platform.audit','UPDATE') OR has_table_privilege(current_user,'platform.eligibility_requests','UPDATE') FROM pg_roles WHERE rolname=current_user`).Scan(&unsafe)
	if err != nil || unsafe {
		return e.ErrUnavailable
	}
	return r.Ready(ctx)
}

// Cleanup removes at most 200 expired records per call. Current configuration
// and installation facts have no count cap and are never removed by retention.
func (r *Repository) Cleanup(ctx context.Context, now time.Time) (int64, error) {
	call, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := r.DB.BeginTx(call, nil)
	if err != nil {
		return 0, e.ErrUnavailable
	}
	defer tx.Rollback()
	var count int64
	for _, query := range []struct {
		sql    string
		cutoff time.Time
	}{
		{`DELETE FROM platform.eligibility_requests WHERE (actor,key) IN (SELECT actor,key FROM platform.eligibility_requests WHERE created_at <= $1 ORDER BY created_at LIMIT 100)`, now.Add(-24 * time.Hour)},
		{`DELETE FROM platform.audit WHERE id IN (SELECT id FROM platform.audit WHERE created_at <= $1 ORDER BY created_at LIMIT 100)`, now.AddDate(-1, 0, 0)},
	} {
		result, err := tx.ExecContext(call, query.sql, query.cutoff)
		if err != nil {
			return 0, e.ErrUnavailable
		}
		n, err := result.RowsAffected()
		if err != nil {
			return 0, e.ErrUnavailable
		}
		count += n
	}
	if err = tx.Commit(); err != nil {
		return 0, e.ErrUnavailable
	}
	return count, nil
}

func unavailable(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return e.ErrUnavailable
}
