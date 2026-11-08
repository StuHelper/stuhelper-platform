package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	a "github.com/StuHelper/stuhelper-platform/admin"
	"github.com/jackc/pgx/v5/pgconn"
)

type adminStore struct{ tx *sql.Tx }

func adminError(e error) error {
	if e == nil {
		return nil
	}
	for _, known := range []error{a.ErrConflict, a.ErrNotFound, a.ErrForbidden, a.ErrInvalid, a.ErrUnavailable, context.Canceled, context.DeadlineExceeded} {
		if errors.Is(e, known) {
			return e
		}
	}
	var pg *pgconn.PgError
	if errors.As(e, &pg) && pg.Code == "23505" {
		return a.ErrConflict
	}
	return a.ErrUnavailable
}
func (r *Repository) adminTransaction(ctx context.Context, write bool, fn func(a.Store) error) error {
	opts := &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}
	if write {
		opts = &sql.TxOptions{Isolation: sql.LevelReadCommitted}
	}
	tx, e := r.DB.BeginTx(ctx, opts)
	if e != nil {
		return adminError(e)
	}
	defer tx.Rollback()
	// Serialize the permission check with all grant changes to prevent a revoked
	// delegator or concurrent grant from committing based on an obsolete decision.
	if write {
		if _, e = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(726391840113)`); e != nil {
			return adminError(e)
		}
	}
	if e = fn(adminStore{tx}); e != nil {
		return adminError(e)
	}
	return adminError(tx.Commit())
}
func (r *Repository) View(ctx context.Context, fn func(a.Store) error) error {
	return r.adminTransaction(ctx, false, fn)
}
func (r *Repository) Write(ctx context.Context, fn func(a.Store) error) error {
	return r.adminTransaction(ctx, true, fn)
}
func (s adminStore) Role(ctx context.Context, id string) (out a.Role, err error) {
	var raw []byte
	err = s.tx.QueryRowContext(ctx, `SELECT definition FROM platform.admin_roles WHERE id=$1`, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return out, a.ErrNotFound
	}
	if err != nil {
		return out, adminError(err)
	}
	if json.Unmarshal(raw, &out) != nil {
		return out, a.ErrUnavailable
	}
	return
}
func (s adminStore) Effective(ctx context.Context, subject string) (a.Effective, error) {
	out := a.Effective{Subject: subject, Revision: "0", Grants: []a.Grant{}}
	var revision int64
	e := s.tx.QueryRowContext(ctx, `SELECT revision FROM platform.admin_subjects WHERE subject=$1`, subject).Scan(&revision)
	if errors.Is(e, sql.ErrNoRows) {
		return out, nil
	}
	if e != nil {
		return out, adminError(e)
	}
	out.Revision = strconv.FormatInt(revision, 10)
	rows, e := s.tx.QueryContext(ctx, `SELECT r.definition,g.school,g.group_id,g.module FROM platform.admin_grants g JOIN platform.admin_roles r ON r.id=g.role_id WHERE g.subject=$1 ORDER BY g.role_id,g.school,g.group_id,g.module LIMIT 1001`, subject)
	if e != nil {
		return out, adminError(e)
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		var g a.Grant
		if rows.Scan(&raw, &g.Scope.School, &g.Scope.Group, &g.Scope.Module) != nil || json.Unmarshal(raw, &g.Role) != nil {
			return out, a.ErrUnavailable
		}
		out.Grants = append(out.Grants, g)
	}
	if len(out.Grants) > 1000 {
		return out, a.ErrUnavailable
	}
	return out, adminError(rows.Err())
}
func (s adminStore) CreateRole(ctx context.Context, r a.Role) error {
	var count int
	if e := s.tx.QueryRowContext(ctx, `SELECT count(*) FROM platform.admin_roles`).Scan(&count); e != nil {
		return adminError(e)
	}
	if count >= 1000 {
		return a.ErrConflict
	}
	b, _ := json.Marshal(r)
	_, e := s.tx.ExecContext(ctx, `INSERT INTO platform.admin_roles(id,definition) VALUES($1,$2)`, r.ID, b)
	return adminError(e)
}
func (s adminStore) Change(ctx context.Context, subject string, m a.Mutation, revoke bool) (a.Revision, error) {
	out := a.Revision{}
	if _, e := s.tx.ExecContext(ctx, `INSERT INTO platform.admin_subjects(subject) VALUES($1) ON CONFLICT DO NOTHING`, subject); e != nil {
		return out, adminError(e)
	}
	var revision int64
	if e := s.tx.QueryRowContext(ctx, `SELECT revision FROM platform.admin_subjects WHERE subject=$1 FOR UPDATE`, subject).Scan(&revision); e != nil {
		return out, adminError(e)
	}
	if strconv.FormatInt(revision, 10) != m.ExpectedRevision {
		return out, a.ErrConflict
	}
	query := `INSERT INTO platform.admin_grants(subject,role_id,school,group_id,module) VALUES($1,$2,$3,$4,$5)`
	if revoke {
		query = `DELETE FROM platform.admin_grants WHERE subject=$1 AND role_id=$2 AND school=$3 AND group_id=$4 AND module=$5`
	} else {
		var count int
		if e := s.tx.QueryRowContext(ctx, `SELECT count(*) FROM platform.admin_grants WHERE subject=$1`, subject).Scan(&count); e != nil {
			return out, adminError(e)
		}
		if count >= 1000 {
			return out, a.ErrConflict
		}
	}
	result, e := s.tx.ExecContext(ctx, query, subject, m.RoleID, m.Scope.School, m.Scope.Group, m.Scope.Module)
	if e != nil {
		return out, adminError(e)
	}
	count, e := result.RowsAffected()
	if e != nil {
		return out, adminError(e)
	}
	if count != 1 {
		return out, a.ErrConflict
	}
	if e = s.tx.QueryRowContext(ctx, `UPDATE platform.admin_subjects SET revision=revision+1 WHERE subject=$1 RETURNING revision`, subject).Scan(&revision); e != nil {
		return out, adminError(e)
	}
	out.Revision = strconv.FormatInt(revision, 10)
	return out, nil
}
func (s adminStore) Record(ctx context.Context, r a.Record) error {
	raw, _ := json.Marshal(r)
	_, e := s.tx.ExecContext(ctx, `INSERT INTO platform.audit(actor,action,target,data,created_at,school,group_id,module,reason,result) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, r.Actor, r.Action, r.Target, raw, r.CreatedAt, r.Scope.School, r.Scope.Group, r.Scope.Module, r.Reason, r.Result)
	return adminError(e)
}
func (s adminStore) Replay(ctx context.Context, actor, key, hash string) ([]byte, error) {
	var previous string
	var b []byte
	e := s.tx.QueryRowContext(ctx, `SELECT digest,result FROM platform.admin_requests WHERE actor=$1 AND key=$2`, actor, key).Scan(&previous, &b)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, adminError(e)
	}
	if previous != hash {
		return nil, a.ErrConflict
	}
	return b, nil
}
func (s adminStore) SaveReplay(ctx context.Context, actor, key, hash string, b []byte, at time.Time) error {
	_, e := s.tx.ExecContext(ctx, `INSERT INTO platform.admin_requests(actor,key,digest,result,created_at) VALUES($1,$2,$3,$4,$5)`, actor, key, hash, b, at)
	return adminError(e)
}

func nullableTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}
