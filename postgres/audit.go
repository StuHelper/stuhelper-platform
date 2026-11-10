package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strconv"

	a "github.com/StuHelper/stuhelper-platform/admin"
)

// PostgreSQL visibility pins committed membership, including transactions that
// allocated an ID before the first page but had not yet committed. Retention
// changes invalidate the snapshot instead of silently omitting removed rows.
func (s adminStore) ReadAudit(ctx context.Context, q a.AuditRequest) (a.AuditBatch, error) {
	out := a.AuditBatch{Records: []a.Record{}}
	var epoch int64
	if err := s.tx.QueryRowContext(ctx, `SELECT revision FROM platform.audit_retention WHERE singleton`).Scan(&epoch); err != nil {
		return out, adminError(err)
	}
	var visibility string
	if q.Snapshot == "" {
		if err := s.tx.QueryRowContext(ctx, `SELECT pg_current_snapshot()::text`).Scan(&visibility); err != nil {
			return out, adminError(err)
		}

		// Persist only visibility metadata outside the read-only view transaction.
		// Failed reads may leave a cache entry; capacity and expiry bound its lifetime.
		token := make([]byte, 16)
		if _, err := rand.Read(token); err != nil {
			return out, a.ErrUnavailable
		}
		out.Snapshot = hex.EncodeToString(token)
		if _, err := s.snapshots.ExecContext(ctx, `INSERT INTO platform.audit_snapshots(slot,token,visibility,epoch,created_at)
 VALUES ((nextval('platform.audit_snapshot_slot') % 4096)::integer,$1,$2::pg_snapshot,$3,clock_timestamp())
 ON CONFLICT(slot) DO UPDATE SET token=EXCLUDED.token,visibility=EXCLUDED.visibility,epoch=EXCLUDED.epoch,created_at=EXCLUDED.created_at`, out.Snapshot, visibility, epoch); err != nil {
			return out, adminError(err)
		}
	} else {
		if len(q.Snapshot) != 32 {
			return out, a.ErrUnavailable
		}
		if _, err := hex.DecodeString(q.Snapshot); err != nil {
			return out, a.ErrUnavailable
		}
		if err := s.tx.QueryRowContext(ctx, `SELECT visibility::text FROM platform.audit_snapshots WHERE token=$1 AND epoch=$2 AND created_at > statement_timestamp()-interval '24 hours'`, q.Snapshot, epoch).Scan(&visibility); err != nil {
			return out, adminError(err)
		}
		out.Snapshot = q.Snapshot
	}
	scopes, _ := json.Marshal(q.Scopes)
	var beforeTime any
	beforeSource, beforeID := "", ""
	if q.Before != nil {
		beforeTime = q.Before.Time
		beforeSource = q.Before.Source
		beforeID = q.Before.ID
	}
	rows, err := s.tx.QueryContext(ctx, `SELECT id,actor,action,target,school,group_id,module,reason,result,created_at,COALESCE(data->>'role_id',''),COALESCE(data->>'revision','')
 FROM platform.audit a WHERE pg_visible_in_snapshot(recorded_xid,$1::pg_snapshot) AND EXISTS (SELECT 1 FROM jsonb_to_recordset($2::jsonb) AS s(school text,"group" text,module text) WHERE (s.school='*' OR a.school=s.school) AND (s."group"='*' OR a.group_id=s."group") AND (s.module='*' OR a.module=s.module))
 AND ($3='' OR target=$3) AND ($4='' OR actor=$4) AND ($5='' OR action=$5)
 AND ($6::timestamptz IS NULL OR created_at >= $6) AND created_at < $7
 AND ($8::timestamptz IS NULL OR (created_at,'platform' COLLATE "C",id::text COLLATE "C") < ($8,$9::text COLLATE "C",$10::text COLLATE "C"))
 ORDER BY created_at DESC,id::text COLLATE "C" DESC LIMIT $11`, visibility, scopes, q.Target, q.Actor, q.Action, nullableTime(q.From), q.Until, beforeTime, beforeSource, beforeID, q.Limit)
	if err != nil {
		return out, adminError(err)
	}
	defer rows.Close()
	for rows.Next() {
		r := a.Record{Source: "platform"}
		var id int64
		if err = rows.Scan(&id, &r.Actor, &r.Action, &r.Target, &r.Scope.School, &r.Scope.Group, &r.Scope.Module, &r.Reason, &r.Result, &r.CreatedAt, &r.RoleID, &r.Revision); err != nil {
			return out, adminError(err)
		}
		r.ID = strconv.FormatInt(id, 10)
		out.Records = append(out.Records, r)
	}
	return out, adminError(rows.Err())
}
