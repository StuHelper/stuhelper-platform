package postgres

import (
	"context"
	"database/sql"
	"errors"
	h "github.com/StuHelper/stuhelper-platform/modulehistory"
	"regexp"
	"time"
)

var identifier = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

func (r *Repository) State(ctx context.Context, name string) (h.State, error) {
	if !identifier.MatchString(name) {
		return h.Unknown, h.ErrUnavailable
	}
	call, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var state h.State
	err := r.DB.QueryRowContext(call, `SELECT state FROM platform.installations WHERE module=$1`, name).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return h.Unknown, nil
	}
	if err != nil || state != h.Installed && state != h.NeverInstalled {
		return h.Unknown, h.ErrUnavailable
	}
	return state, nil
}

// Initialize records explicitly verified absence. Existing facts cannot be
// replaced; in particular an installed module can never become never-installed.
func (r *Repository) Initialize(ctx context.Context, names []string) error {
	return r.record(ctx, names, h.NeverInstalled)
}
func (r *Repository) ObserveInstalled(ctx context.Context, names []string) error {
	return r.record(ctx, names, h.Installed)
}
func (r *Repository) record(ctx context.Context, names []string, state h.State) error {
	if len(names) > 200 {
		return h.ErrUnavailable
	}
	seen := map[string]bool{}
	for _, name := range names {
		if !identifier.MatchString(name) || seen[name] {
			return h.ErrUnavailable
		}
		seen[name] = true
	}
	call, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	tx, err := r.DB.BeginTx(call, nil)
	if err != nil {
		return h.ErrUnavailable
	}
	defer tx.Rollback()
	// Serializes installation with explicit absence initialization in all processes.
	if _, err = tx.ExecContext(call, `SELECT pg_advisory_xact_lock(726391840112)`); err != nil {
		return h.ErrUnavailable
	}
	for _, name := range names {
		var old h.State
		err = tx.QueryRowContext(call, `SELECT state FROM platform.installations WHERE module=$1`, name).Scan(&old)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return h.ErrUnavailable
		}
		if old != "" && state == h.NeverInstalled {
			return h.ErrUnavailable
		}
		if old == h.Installed {
			continue
		}
		if _, err = tx.ExecContext(call, `INSERT INTO platform.installations VALUES($1,$2) ON CONFLICT(module) DO UPDATE SET state=EXCLUDED.state`, name, state); err != nil {
			return h.ErrUnavailable
		}
		if _, err = tx.ExecContext(call, `INSERT INTO platform.audit(actor,action,target,data,created_at) VALUES('deployment','installation.record',$1,jsonb_build_object('state',$2::text),now())`, name, state); err != nil {
			return h.ErrUnavailable
		}
	}
	if tx.Commit() != nil {
		return h.ErrUnavailable
	}
	return nil
}
