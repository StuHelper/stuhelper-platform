package postgres

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

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
	} else {
		previous, token, ok := strings.Cut(q.Snapshot, "|")
		if !ok || previous != strconv.FormatInt(epoch, 10) {
			return out, a.ErrUnavailable
		}
		visibility = token
	}
	out.Snapshot = strconv.FormatInt(epoch, 10) + "|" + visibility
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
