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
)

//go:embed schema.sql
var Schema string

type Repository struct{ DB *sql.DB }

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
func (r *Repository) Ready(ctx context.Context) error {
	var revision int64
	if err := r.DB.QueryRowContext(ctx, `SELECT revision FROM platform.configuration WHERE singleton`).Scan(&revision); err != nil || revision < 1 {
		return e.ErrUnavailable
	}
	return nil
}
func (r *Repository) CheckRuntime(ctx context.Context) error {
	var unsafe bool
	err := r.DB.QueryRowContext(ctx, `SELECT rolsuper OR rolcreaterole OR rolcreatedb OR has_schema_privilege(current_user,'platform','CREATE') OR has_table_privilege(current_user,'platform.audit','UPDATE') OR has_table_privilege(current_user,'platform.eligibility_requests','UPDATE') OR has_table_privilege(current_user,'platform.admin_roles','UPDATE') OR has_table_privilege(current_user,'platform.admin_roles','DELETE') OR has_table_privilege(current_user,'platform.admin_requests','UPDATE') FROM pg_roles WHERE rolname=current_user`).Scan(&unsafe)
	if err != nil || unsafe {
		return e.ErrUnavailable
	}
	return r.Ready(ctx)
}

// Cleanup removes at most 400 expired records per call. Current configuration
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
		audit  bool
	}{
		{`DELETE FROM platform.eligibility_requests WHERE (actor,key) IN (SELECT actor,key FROM platform.eligibility_requests WHERE created_at <= $1 ORDER BY created_at LIMIT 100)`, now.Add(-24 * time.Hour), false},
		{`DELETE FROM platform.audit_snapshots WHERE slot IN (SELECT slot FROM platform.audit_snapshots WHERE created_at <= $1 ORDER BY created_at LIMIT 100)`, now.Add(-24 * time.Hour), false},
		{`DELETE FROM platform.admin_requests WHERE (actor,key) IN (SELECT actor,key FROM platform.admin_requests WHERE created_at <= $1 ORDER BY created_at LIMIT 100)`, now.Add(-24 * time.Hour), false},
		{`DELETE FROM platform.audit WHERE id IN (SELECT id FROM platform.audit WHERE created_at <= $1 ORDER BY created_at LIMIT 100)`, now.AddDate(-1, 0, 0), true},
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
		if n > 0 && query.audit {
			if _, err = tx.ExecContext(call, `UPDATE platform.audit_retention SET revision=revision+1 WHERE singleton`); err != nil {
				return 0, e.ErrUnavailable
			}
		}
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
