package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	a "github.com/StuHelper/stuhelper-platform/admin"
	"github.com/jackc/pgx/v5/pgconn"
)

type pendingAuditSnapshot struct {
	bucket     *int
	token      string
	visibility string
	epoch      int64
}

type adminStore struct {
	tx               *sql.Tx
	pendingSnapshots []pendingAuditSnapshot
}

func adminError(e error) error {
	if e == nil {
		return nil
	}
	for _, known := range []error{a.ErrConflict, a.ErrNotFound, a.ErrForbidden, a.ErrInvalid, a.ErrSnapshotExpired, a.ErrUnavailable, context.Canceled, context.DeadlineExceeded} {
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
	store := &adminStore{tx: tx}
	if e = fn(store); e != nil {
		return adminError(e)
	}
	if e = tx.Commit(); e != nil {
		return adminError(e)
	}
	// Persist visibility snapshots only after the read-only transaction commits and
	// releases its connection back to r.DB, preventing connection-pool self-deadlock.
	for _, snap := range store.pendingSnapshots {
		if err := r.persistAuditSnapshot(ctx, snap); err != nil {
			return adminError(err)
		}
	}
	return nil
}

func (r *Repository) persistAuditSnapshot(ctx context.Context, snap pendingAuditSnapshot) error {
	tx, err := r.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var slot int
	if snap.bucket == nil {
		err = tx.QueryRowContext(ctx, `SELECT nextval('platform.audit_snapshot_slot') % 4096`).Scan(&slot)
	} else {
		// Allocate after acquiring the bucket lock. Under READ COMMITTED the next
		// statement sees slots committed by the previous holder on any instance.
		if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(726391840, $1)`, *snap.bucket); err != nil {
			return err
		}
		err = tx.QueryRowContext(ctx, `
SELECT candidate.slot
FROM generate_series($1::int, $1::int + 63) AS candidate(slot)
LEFT JOIN platform.audit_snapshots AS saved USING (slot)
ORDER BY saved.created_at NULLS FIRST, candidate.slot
LIMIT 1`, *snap.bucket*64).Scan(&slot)
	}
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO platform.audit_snapshots(slot,token,visibility,epoch,created_at)
VALUES ($1,$2,$3::pg_snapshot,$4,clock_timestamp())
ON CONFLICT(slot) DO UPDATE SET token=EXCLUDED.token,visibility=EXCLUDED.visibility,epoch=EXCLUDED.epoch,created_at=EXCLUDED.created_at`, slot, snap.token, snap.visibility, snap.epoch)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) View(ctx context.Context, fn func(a.Store) error) error {
	return r.adminTransaction(ctx, false, fn)
}
func (r *Repository) Write(ctx context.Context, fn func(a.Store) error) error {
	return r.adminTransaction(ctx, true, fn)
}
func (s *adminStore) Role(ctx context.Context, id string) (out a.Role, err error) {
	var raw []byte
	var createdAt time.Time
	err = s.tx.QueryRowContext(ctx, `SELECT definition,created_at FROM platform.admin_roles WHERE id=$1`, id).Scan(&raw, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return out, a.ErrNotFound
	}
	if err != nil {
		return out, adminError(err)
	}
	if json.Unmarshal(raw, &out) != nil {
		return out, a.ErrUnavailable
	}
	at := createdAt.UTC()
	out.CreatedAt = &at
	return
}

func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

func (s *adminStore) ListRoles(ctx context.Context, q a.RoleQuery) ([]a.Role, error) {
	out := []a.Role{}
	var beforeTime any
	beforeID := ""
	if q.Before != nil {
		beforeTime = q.Before.CreatedAt.UTC()
		beforeID = q.Before.ID
	}
	pattern := ""
	if q.Keyword != "" {
		pattern = "%" + escapeLike(q.Keyword) + "%"
	}
	rows, err := s.tx.QueryContext(ctx, `SELECT definition,created_at FROM platform.admin_roles
 WHERE ($1='' OR id ILIKE $1 ESCAPE '\' OR (definition->>'name') ILIKE $1 ESCAPE '\')
 AND ($2='' OR (definition->'capabilities') @> to_jsonb($2::text))
 AND ($3::timestamptz IS NULL OR (created_at,id COLLATE "C") < ($3,$4::text COLLATE "C"))
 ORDER BY created_at DESC,id COLLATE "C" DESC LIMIT $5`, pattern, q.Capability, beforeTime, beforeID, q.Limit)
	if err != nil {
		return out, adminError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		var createdAt time.Time
		var role a.Role
		if rows.Scan(&raw, &createdAt) != nil || json.Unmarshal(raw, &role) != nil {
			return out, a.ErrUnavailable
		}
		at := createdAt.UTC()
		role.CreatedAt = &at
		out = append(out, role)
	}
	return out, adminError(rows.Err())
}
func (s *adminStore) Effective(ctx context.Context, subject string) (a.Effective, error) {
	out := a.Effective{Subject: subject, Revision: "0", Grants: []a.Grant{}, AuthorizationMode: a.ExplicitGrants}
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
func (s *adminStore) CreateRole(ctx context.Context, r a.Role) error {
	var count int
	if e := s.tx.QueryRowContext(ctx, `SELECT count(*) FROM platform.admin_roles`).Scan(&count); e != nil {
		return adminError(e)
	}
	if count >= 1000 {
		return a.ErrConflict
	}
	stored := r
	stored.CreatedAt = nil
	b, _ := json.Marshal(stored)
	at := time.Now().UTC()
	if r.CreatedAt != nil {
		at = r.CreatedAt.UTC()
	}
	_, e := s.tx.ExecContext(ctx, `INSERT INTO platform.admin_roles(id,definition,created_at) VALUES($1,$2,$3)`, r.ID, b, at)
	return adminError(e)
}

func (s *adminStore) ListAdministrators(ctx context.Context, q a.AdministratorQuery) ([]a.AdministratorSummary, error) {
	out := []a.AdministratorSummary{}
	scopes, _ := json.Marshal(q.Scopes)
	rows, err := s.tx.QueryContext(ctx, `SELECT g.subject,sub.revision
 FROM platform.admin_grants g
 JOIN platform.admin_subjects sub ON sub.subject=g.subject
 JOIN platform.admin_roles r ON r.id=g.role_id
 WHERE ($1='' OR g.role_id=$1)
 AND ($2='' OR (r.definition->'capabilities') @> to_jsonb($2::text))
 AND ($3='' OR g.subject COLLATE "C" > $3 COLLATE "C")
 AND EXISTS (SELECT 1 FROM jsonb_to_recordset($4::jsonb) AS p(school text,"group" text,module text)
  WHERE (p.school='*' OR g.school='*' OR g.school=p.school)
  AND (p."group"='*' OR g.group_id='*' OR g.group_id=p."group")
  AND (p.module='*' OR g.module='*' OR g.module=p.module))
 GROUP BY g.subject,sub.revision
 ORDER BY g.subject COLLATE "C" ASC LIMIT $5`, q.RoleID, q.Capability, q.AfterSubject, scopes, q.Limit)
	if err != nil {
		return out, adminError(err)
	}
	subjects := []string{}
	indexBySubject := map[string]int{}
	for rows.Next() {
		var subject string
		var revision int64
		if rows.Scan(&subject, &revision) != nil {
			rows.Close()
			return out, a.ErrUnavailable
		}
		indexBySubject[subject] = len(out)
		subjects = append(subjects, subject)
		out = append(out, a.AdministratorSummary{Subject: subject, Revision: strconv.FormatInt(revision, 10), Grants: []a.Grant{}})
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return out, adminError(err)
	}
	rows.Close()
	if len(subjects) == 0 {
		return out, nil
	}
	grantRows, err := s.tx.QueryContext(ctx, `SELECT g.subject,r.definition,g.school,g.group_id,g.module
 FROM platform.admin_grants g
 JOIN platform.admin_roles r ON r.id=g.role_id
 WHERE g.subject = ANY($1::text[])
 AND ($2='' OR g.role_id=$2)
 AND ($3='' OR (r.definition->'capabilities') @> to_jsonb($3::text))
 AND EXISTS (SELECT 1 FROM jsonb_to_recordset($4::jsonb) AS p(school text,"group" text,module text)
  WHERE (p.school='*' OR g.school='*' OR g.school=p.school)
  AND (p."group"='*' OR g.group_id='*' OR g.group_id=p."group")
  AND (p.module='*' OR g.module='*' OR g.module=p.module))
 ORDER BY g.subject COLLATE "C" ASC,g.role_id,g.school,g.group_id,g.module`, subjects, q.RoleID, q.Capability, scopes)
	if err != nil {
		return out, adminError(err)
	}
	defer grantRows.Close()
	for grantRows.Next() {
		var subject string
		var raw []byte
		var g a.Grant
		if grantRows.Scan(&subject, &raw, &g.Scope.School, &g.Scope.Group, &g.Scope.Module) != nil || json.Unmarshal(raw, &g.Role) != nil {
			return out, a.ErrUnavailable
		}
		idx, ok := indexBySubject[subject]
		if !ok {
			return out, a.ErrUnavailable
		}
		out[idx].Grants = append(out[idx].Grants, g)
	}
	return out, adminError(grantRows.Err())
}

func (s *adminStore) ApplyGrantChanges(ctx context.Context, subject, expectedRevision string, add, revoke []a.GrantTarget, actor, reason string, at time.Time) (a.Revision, error) {
	if _, e := s.tx.ExecContext(ctx, `SAVEPOINT admin_grant_change`); e != nil {
		return a.Revision{}, adminError(e)
	}
	abort := func(err error) (a.Revision, error) {
		_, _ = s.tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT admin_grant_change`)
		return a.Revision{}, adminError(err)
	}
	if _, e := s.tx.ExecContext(ctx, `INSERT INTO platform.admin_subjects(subject) VALUES($1) ON CONFLICT DO NOTHING`, subject); e != nil {
		return abort(e)
	}
	var revision int64
	if e := s.tx.QueryRowContext(ctx, `SELECT revision FROM platform.admin_subjects WHERE subject=$1 FOR UPDATE`, subject).Scan(&revision); e != nil {
		return abort(e)
	}
	if strconv.FormatInt(revision, 10) != expectedRevision {
		return abort(a.ErrConflict)
	}
	for _, t := range revoke {
		result, e := s.tx.ExecContext(ctx, `DELETE FROM platform.admin_grants WHERE subject=$1 AND role_id=$2 AND school=$3 AND group_id=$4 AND module=$5`, subject, t.RoleID, t.Scope.School, t.Scope.Group, t.Scope.Module)
		if e != nil {
			return abort(e)
		}
		n, e := result.RowsAffected()
		if e != nil {
			return abort(e)
		}
		if n != 1 {
			return abort(a.ErrConflict)
		}
	}
	if len(add) > 0 {
		var count int
		if e := s.tx.QueryRowContext(ctx, `SELECT count(*) FROM platform.admin_grants WHERE subject=$1`, subject).Scan(&count); e != nil {
			return abort(e)
		}
		if count+len(add) > 1000 {
			return abort(a.ErrConflict)
		}
		for _, t := range add {
			if _, e := s.tx.ExecContext(ctx, `INSERT INTO platform.admin_grants(subject,role_id,school,group_id,module) VALUES($1,$2,$3,$4,$5)`, subject, t.RoleID, t.Scope.School, t.Scope.Group, t.Scope.Module); e != nil {
				return abort(e)
			}
		}
	}
	if e := s.tx.QueryRowContext(ctx, `UPDATE platform.admin_subjects SET revision=revision+1 WHERE subject=$1 RETURNING revision`, subject).Scan(&revision); e != nil {
		return abort(e)
	}
	revStr := strconv.FormatInt(revision, 10)
	for _, t := range revoke {
		if e := s.Record(ctx, a.Record{Source: "platform", Actor: actor, Action: "role.revoke", Target: subject, RoleID: t.RoleID, Revision: revStr, Scope: t.Scope, Reason: reason, Result: "success", CreatedAt: at}); e != nil {
			return abort(e)
		}
	}
	for _, t := range add {
		if e := s.Record(ctx, a.Record{Source: "platform", Actor: actor, Action: "role.grant", Target: subject, RoleID: t.RoleID, Revision: revStr, Scope: t.Scope, Reason: reason, Result: "success", CreatedAt: at}); e != nil {
			return abort(e)
		}
	}
	if _, e := s.tx.ExecContext(ctx, `RELEASE SAVEPOINT admin_grant_change`); e != nil {
		return a.Revision{}, adminError(e)
	}
	return a.Revision{Revision: revStr}, nil
}
func (s *adminStore) Change(ctx context.Context, subject string, m a.Mutation, revoke bool) (a.Revision, error) {
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
func (s *adminStore) Record(ctx context.Context, r a.Record) error {
	raw, _ := json.Marshal(r)
	_, e := s.tx.ExecContext(ctx, `INSERT INTO platform.audit(actor,action,target,data,created_at,school,group_id,module,reason,result) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, r.Actor, r.Action, r.Target, raw, r.CreatedAt, r.Scope.School, r.Scope.Group, r.Scope.Module, r.Reason, r.Result)
	return adminError(e)
}
func (s *adminStore) Replay(ctx context.Context, actor, key, hash string) ([]byte, error) {
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
func (s *adminStore) SaveReplay(ctx context.Context, actor, key, hash string, b []byte, at time.Time) error {
	_, e := s.tx.ExecContext(ctx, `INSERT INTO platform.admin_requests(actor,key,digest,result,created_at) VALUES($1,$2,$3,$4,$5)`, actor, key, hash, b, at)
	return adminError(e)
}

func nullableTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}
