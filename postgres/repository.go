// Package postgres persists platform configuration and installation facts.
package postgres

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	e "github.com/StuHelper/stuhelper-platform/eligibility"
	"github.com/jackc/pgx/v5/pgconn"
	"log/slog"
	"sync"
)

//go:embed schema.sql
var Schema string

type Repository struct {
	DB       *sql.DB
	Logger   *slog.Logger
	Now      func() time.Time
	ReadyTTL time.Duration

	readyMu    sync.Mutex
	readyUntil time.Time
	readyWait  chan struct{}
	readyErr   error
}

func (r *Repository) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Repository) logFailure(operation string, err error) {
	if r == nil || r.Logger == nil || err == nil || errors.Is(err, context.Canceled) {
		return
	}
	reason := "database_error"
	if errors.Is(err, context.DeadlineExceeded) {
		reason = "timeout"
	}
	attrs := []any{"operation", operation, "reason", reason}
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code != "" {
		attrs = append(attrs, "sqlstate", pg.Code)
	}
	r.Logger.Error("platform database failure", attrs...)
}

type migrationStep struct {
	version int
	sql     string
}

func parseMigrations(raw string) ([]migrationStep, error) {
	var steps []migrationStep
	var current migrationStep
	var buf strings.Builder
	flush := func() error {
		if current.version == 0 {
			return nil
		}
		stmt := strings.TrimSpace(buf.String())
		if stmt == "" {
			return fmt.Errorf("empty migration %d", current.version)
		}
		current.sql = stmt
		steps = append(steps, current)
		buf.Reset()
		return nil
	}
	for _, line := range strings.Split(raw, "\n") {
		if after, ok := strings.CutPrefix(strings.TrimSpace(line), "-- +migration "); ok {
			if err := flush(); err != nil {
				return nil, err
			}
			v, err := strconv.Atoi(strings.TrimSpace(after))
			if err != nil || v <= 0 || (len(steps) > 0 && v != steps[len(steps)-1].version+1) {
				return nil, fmt.Errorf("invalid migration marker: %q", line)
			}
			current = migrationStep{version: v}
			continue
		}
		buf.WriteString(line)
		buf.WriteByte('\n')
	}
	if err := flush(); err != nil {
		return nil, err
	}
	if len(steps) == 0 {
		return nil, errors.New("no schema migrations declared")
	}
	return steps, nil
}

func Migrate(ctx context.Context, db *sql.DB) error {
	steps, err := parseMigrations(Schema)
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(726391840111)`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS platform; CREATE TABLE IF NOT EXISTS platform.schema_migrations(version integer PRIMARY KEY CHECK(version>0), applied_at timestamptz NOT NULL DEFAULT now()); REVOKE ALL ON SCHEMA platform FROM PUBLIC; REVOKE ALL ON platform.schema_migrations FROM PUBLIC;`); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT version FROM platform.schema_migrations`)
	if err != nil {
		return err
	}
	applied := map[int]bool{}
	for rows.Next() {
		var v int
		if err = rows.Scan(&v); err != nil {
			rows.Close()
			return err
		}
		applied[v] = true
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, step := range steps {
		if applied[step.version] {
			continue
		}
		if _, err = tx.ExecContext(ctx, step.sql); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO platform.schema_migrations(version,applied_at) VALUES($1,now())`, step.version); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (r *Repository) queryReady(ctx context.Context) error {
	if r == nil || r.DB == nil {
		return e.ErrUnavailable
	}
	var revision int64
	if err := r.DB.QueryRowContext(ctx, `SELECT revision FROM platform.configuration WHERE singleton`).Scan(&revision); err != nil || revision < 1 {
		if err != nil {
			r.logFailure("ready", err)
		}
		return e.ErrUnavailable
	}
	return nil
}

// Ready coalesces concurrent probes and caches positive database readiness for a
// short bounded window (default 1s; set ReadyTTL < 0 to disable caching).
func (r *Repository) Ready(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r == nil || r.DB == nil {
		return e.ErrUnavailable
	}
	ttl := r.ReadyTTL
	if ttl == 0 {
		ttl = time.Second
	}
	r.readyMu.Lock()
	if ttl > 0 && r.now().Before(r.readyUntil) {
		r.readyMu.Unlock()
		return nil
	}
	if r.readyWait != nil {
		wait := r.readyWait
		r.readyMu.Unlock()
		select {
		case <-wait:
			r.readyMu.Lock()
			err := r.readyErr
			r.readyMu.Unlock()
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	wait := make(chan struct{})
	r.readyWait = wait
	r.readyMu.Unlock()

	probeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	err := r.queryReady(probeCtx)
	cancel()

	r.readyMu.Lock()
	r.readyErr = err
	if err == nil && ttl > 0 {
		r.readyUntil = r.now().Add(ttl)
	} else {
		r.readyUntil = time.Time{}
	}
	r.readyWait = nil
	close(wait)
	r.readyMu.Unlock()

	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

const runtimePrivilegeCheckSQL = `SELECT
 (rolsuper OR rolcreaterole OR rolcreatedb
  OR has_schema_privilege(current_user,'platform','CREATE')
  OR has_table_privilege(current_user,'platform.audit','UPDATE')
  OR has_table_privilege(current_user,'platform.eligibility_requests','UPDATE')
  OR has_table_privilege(current_user,'platform.admin_roles','UPDATE')
  OR has_table_privilege(current_user,'platform.admin_roles','DELETE')
  OR has_table_privilege(current_user,'platform.admin_requests','UPDATE')),
 (has_schema_privilege(current_user,'platform','USAGE')
  AND has_table_privilege(current_user,'platform.configuration','SELECT')
  AND has_table_privilege(current_user,'platform.configuration','UPDATE')
  AND has_table_privilege(current_user,'platform.eligibility_rules','SELECT')
  AND has_table_privilege(current_user,'platform.eligibility_rules','INSERT')
  AND has_table_privilege(current_user,'platform.eligibility_rules','UPDATE')
  AND has_table_privilege(current_user,'platform.eligibility_requests','SELECT')
  AND has_table_privilege(current_user,'platform.eligibility_requests','INSERT')
  AND has_table_privilege(current_user,'platform.eligibility_requests','DELETE')
  AND has_table_privilege(current_user,'platform.installations','SELECT')
  AND has_table_privilege(current_user,'platform.installations','INSERT')
  AND has_table_privilege(current_user,'platform.installations','UPDATE')
  AND has_table_privilege(current_user,'platform.audit','SELECT')
  AND has_table_privilege(current_user,'platform.audit','INSERT')
  AND has_table_privilege(current_user,'platform.audit','DELETE')
  AND has_table_privilege(current_user,'platform.admin_roles','SELECT')
  AND has_table_privilege(current_user,'platform.admin_roles','INSERT')
  AND has_table_privilege(current_user,'platform.admin_subjects','SELECT')
  AND has_table_privilege(current_user,'platform.admin_subjects','INSERT')
  AND has_table_privilege(current_user,'platform.admin_subjects','UPDATE')
  AND has_table_privilege(current_user,'platform.admin_grants','SELECT')
  AND has_table_privilege(current_user,'platform.admin_grants','INSERT')
  AND has_table_privilege(current_user,'platform.admin_grants','DELETE')
  AND has_table_privilege(current_user,'platform.admin_requests','SELECT')
  AND has_table_privilege(current_user,'platform.admin_requests','INSERT')
  AND has_table_privilege(current_user,'platform.admin_requests','DELETE')
  AND has_table_privilege(current_user,'platform.audit_retention','SELECT')
  AND has_table_privilege(current_user,'platform.audit_retention','UPDATE')
  AND has_table_privilege(current_user,'platform.audit_snapshots','SELECT')
  AND has_table_privilege(current_user,'platform.audit_snapshots','INSERT')
  AND has_table_privilege(current_user,'platform.audit_snapshots','UPDATE')
  AND has_table_privilege(current_user,'platform.audit_snapshots','DELETE')
  AND has_sequence_privilege(current_user,'platform.audit_snapshot_slot','USAGE'))
FROM pg_roles WHERE rolname=current_user`

func (r *Repository) CheckRuntime(ctx context.Context) error {
	if r == nil || r.DB == nil {
		return e.ErrUnavailable
	}
	var unsafe, permitted bool
	err := r.DB.QueryRowContext(ctx, runtimePrivilegeCheckSQL).Scan(&unsafe, &permitted)
	if err != nil {
		r.logFailure("check_runtime", err)
		return e.ErrUnavailable
	}
	if unsafe || !permitted {
		if r.Logger != nil {
			r.Logger.Error("platform database privilege check failed", "operation", "check_runtime", "reason", "db_privilege")
		}
		return e.ErrUnavailable
	}
	if err := r.queryReady(ctx); err != nil {
		r.readyMu.Lock()
		r.readyUntil = time.Time{}
		r.readyMu.Unlock()
		return err
	}
	return nil
}

// Cleanup removes at most 400 expired records per call. Current configuration
// and installation facts have no count cap and are never removed by retention.
func (r *Repository) Cleanup(ctx context.Context, now time.Time) (int64, error) {
	call, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := r.DB.BeginTx(call, nil)
	if err != nil {
		r.logFailure("cleanup_begin", err)
		return 0, e.ErrUnavailable
	}
	defer tx.Rollback()
	var count int64
	for _, query := range []struct {
		sql    string
		cutoff time.Time
		audit  bool
	}{
		{`DELETE FROM platform.eligibility_requests WHERE (actor,key) IN (SELECT actor,key FROM platform.eligibility_requests WHERE created_at <= $1 ORDER BY created_at LIMIT 100)`, now.Add(-24 * time.Hour), false},
		{`DELETE FROM platform.audit_snapshots WHERE slot IN (SELECT slot FROM platform.audit_snapshots WHERE created_at <= $1 ORDER BY created_at LIMIT 100)`, now.Add(-24 * time.Hour), false},
		{`DELETE FROM platform.admin_requests WHERE (actor,key) IN (SELECT actor,key FROM platform.admin_requests WHERE created_at <= $1 ORDER BY created_at LIMIT 100)`, now.Add(-24 * time.Hour), false},
		{`DELETE FROM platform.audit WHERE id IN (SELECT id FROM platform.audit WHERE created_at <= $1 ORDER BY created_at LIMIT 100)`, now.AddDate(-1, 0, 0), true},
	} {
		result, err := tx.ExecContext(call, query.sql, query.cutoff)
		if err != nil {
			r.logFailure("cleanup_delete", err)
			return 0, e.ErrUnavailable
		}
		n, err := result.RowsAffected()
		if err != nil {
			r.logFailure("cleanup_rows", err)
			return 0, e.ErrUnavailable
		}
		count += n
		if n > 0 && query.audit {
			if _, err = tx.ExecContext(call, `UPDATE platform.audit_retention SET revision=revision+1 WHERE singleton`); err != nil {
				r.logFailure("cleanup_retention_revision", err)
				return 0, e.ErrUnavailable
			}
		}
	}
	if err = tx.Commit(); err != nil {
		r.logFailure("cleanup_commit", err)
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
