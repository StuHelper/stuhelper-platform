package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	e "github.com/StuHelper/stuhelper-platform/eligibility"
	"strconv"
	"time"
)

func (r *Repository) Effective(ctx context.Context, action, school string) (e.Rule, error) {
	rule, err := (e.Defaults{}).Effective(ctx, action, school)
	if err != nil {
		return rule, err
	}
	call, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	// One statement observes the global revision and the selected rule together.
	var raw []byte
	var version int64
	err = r.DB.QueryRowContext(call, `SELECT c.revision, (SELECT conditions FROM platform.eligibility_rules WHERE action=$1 AND school IN($2,'*') ORDER BY (school=$2) DESC LIMIT 1) FROM platform.configuration c WHERE c.singleton`, action, school).Scan(&version, &raw)
	if err != nil {
		return e.Rule{}, unavailable(err)
	}
	rule.Version = strconv.FormatInt(version, 10)
	if raw != nil && json.Unmarshal(raw, &rule.Conditions) != nil {
		return e.Rule{}, e.ErrUnavailable
	}
	if e.Validate(rule) != nil {
		return e.Rule{}, e.ErrUnavailable
	}
	return rule, nil
}

func (r *Repository) Apply(ctx context.Context, in e.Update) (e.Rule, error) {
	call, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out := e.Rule{}
	tx, err := r.DB.BeginTx(call, nil)
	if err != nil {
		return out, unavailable(err)
	}
	defer tx.Rollback()
	var revision int64
	if err = tx.QueryRowContext(call, `SELECT revision FROM platform.configuration WHERE singleton FOR UPDATE`).Scan(&revision); err != nil {
		return out, unavailable(err)
	}
	b, _ := json.Marshal([]any{in.School, in.Action, in.Change})
	hash := sha256.Sum256(b)
	digest := hex.EncodeToString(hash[:])
	var previous string
	var result []byte
	var at time.Time
	err = tx.QueryRowContext(call, `SELECT digest,result,created_at FROM platform.eligibility_requests WHERE actor=$1 AND key=$2`, in.Actor, in.Key).Scan(&previous, &result, &at)
	if err == nil && in.At.Before(at.Add(24*time.Hour)) {
		if previous != digest {
			return out, e.ErrConflict
		}
		if json.Unmarshal(result, &out) != nil {
			return out, e.ErrUnavailable
		}
		return out, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, unavailable(err)
	}
	if strconv.FormatInt(revision, 10) != in.Change.ExpectedVersion {
		return out, e.ErrConflict
	}
	out = e.Rule{Version: strconv.FormatInt(revision+1, 10), Conditions: append([]e.Condition{}, in.Change.Conditions...)}
	conditions, _ := json.Marshal(out.Conditions)
	result, _ = json.Marshal(out)
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`DELETE FROM platform.eligibility_requests WHERE actor=$1 AND key=$2`, []any{in.Actor, in.Key}},
		{`UPDATE platform.configuration SET revision=revision+1 WHERE singleton`, nil},
		{`INSERT INTO platform.eligibility_rules VALUES($1,$2,$3) ON CONFLICT(school,action) DO UPDATE SET conditions=EXCLUDED.conditions`, []any{in.School, in.Action, conditions}},
		{`INSERT INTO platform.eligibility_requests VALUES($1,$2,$3,$4,$5)`, []any{in.Actor, in.Key, digest, result, in.At}},
		{`INSERT INTO platform.audit(actor,action,target,data,created_at,school,module) VALUES($1,'eligibility.set',$2,$3,$4,$5,'platform')`, []any{in.Actor, in.School + ":" + in.Action, result, in.At, in.School}},
	} {
		if _, err = tx.ExecContext(call, statement.query, statement.args...); err != nil {
			return e.Rule{}, unavailable(err)
		}
	}
	if err = tx.Commit(); err != nil {
		return e.Rule{}, unavailable(err)
	}
	return out, nil
}
