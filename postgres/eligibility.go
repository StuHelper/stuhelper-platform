package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"time"

	e "github.com/StuHelper/stuhelper-platform/eligibility"
)

func buildEffectiveRule(action, school string, schoolRev int64, globalRaw, schoolRaw []byte) (e.Rule, error) {
	base, err := (e.Defaults{}).Effective(context.Background(), action, school)
	if err != nil {
		return e.Rule{}, err
	}
	globalConditions := slices.Clone(base.Conditions)
	if globalRaw != nil {
		if json.Unmarshal(globalRaw, &globalConditions) != nil {
			return e.Rule{}, e.ErrUnavailable
		}
		if globalConditions == nil {
			globalConditions = []e.Condition{}
		}
	}
	rule := e.Rule{
		Version:             strconv.FormatInt(schoolRev, 10),
		Conditions:          slices.Clone(base.Conditions),
		Source:              e.SourceInitialDefault,
		GlobalConditions:    globalConditions,
		SupportedConditions: base.SupportedConditions,
	}
	if school != "*" && schoolRaw != nil {
		if json.Unmarshal(schoolRaw, &rule.Conditions) != nil {
			return e.Rule{}, e.ErrUnavailable
		}
		if rule.Conditions == nil {
			rule.Conditions = []e.Condition{}
		}
		rule.Source = e.SourceSchoolOverride
	} else if globalRaw != nil {
		rule.Conditions = slices.Clone(globalConditions)
		rule.Source = e.SourceGlobal
	}
	if e.ValidateAction(action, rule) != nil {
		return e.Rule{}, e.ErrUnavailable
	}
	return rule, nil
}

func (r *Repository) Effective(ctx context.Context, action, school string) (e.Rule, error) {
	if !e.ValidSchool(school) || !e.ValidAction(action) {
		return e.Rule{}, e.ErrRule
	}
	call, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var configRev sql.NullInt64
	var schoolRev int64
	var globalRaw, schoolRaw []byte
	err := r.DB.QueryRowContext(call, `SELECT
  (SELECT revision FROM platform.configuration WHERE singleton),
  COALESCE((SELECT revision FROM platform.eligibility_revisions WHERE school=$2), 1),
  (SELECT conditions FROM platform.eligibility_rules WHERE school='*' AND action=$1),
  CASE WHEN $2='*' THEN NULL ELSE (SELECT conditions FROM platform.eligibility_rules WHERE school=$2 AND action=$1) END`,
		action, school).Scan(&configRev, &schoolRev, &globalRaw, &schoolRaw)
	if err != nil {
		return e.Rule{}, unavailable(err)
	}
	if !configRev.Valid || configRev.Int64 < 1 {
		return e.Rule{}, e.ErrUnavailable
	}
	return buildEffectiveRule(action, school, schoolRev, globalRaw, schoolRaw)
}

func (r *Repository) SchoolRules(ctx context.Context, school string) (e.Configuration, error) {
	if !e.ValidSchool(school) {
		return e.Configuration{}, e.ErrRule
	}
	call, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out := e.Configuration{School: school, Rules: map[string]e.Rule{}}
	tx, err := r.DB.BeginTx(call, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return out, unavailable(err)
	}
	defer tx.Rollback()
	var configRev sql.NullInt64
	var schoolRev int64
	if err = tx.QueryRowContext(call, `SELECT
  (SELECT revision FROM platform.configuration WHERE singleton),
  COALESCE((SELECT revision FROM platform.eligibility_revisions WHERE school=$1), 1)`, school).Scan(&configRev, &schoolRev); err != nil {
		return out, unavailable(err)
	}
	if !configRev.Valid || configRev.Int64 < 1 {
		return out, e.ErrUnavailable
	}
	rows, err := tx.QueryContext(call, `SELECT school,action,conditions FROM platform.eligibility_rules WHERE school IN($1,'*')`, school)
	if err != nil {
		return out, unavailable(err)
	}
	defer rows.Close()
	globalByAction := map[string][]byte{}
	schoolByAction := map[string][]byte{}
	for rows.Next() {
		var rowSchool, rowAction string
		var raw []byte
		if err = rows.Scan(&rowSchool, &rowAction, &raw); err != nil {
			return out, unavailable(err)
		}
		if rowSchool == "*" {
			globalByAction[rowAction] = slices.Clone(raw)
		} else if rowSchool == school {
			schoolByAction[rowAction] = slices.Clone(raw)
		}
	}
	if err = rows.Err(); err != nil {
		return out, unavailable(err)
	}
	out.Version = strconv.FormatInt(schoolRev, 10)
	for _, action := range e.Actions() {
		rule, buildErr := buildEffectiveRule(action, school, schoolRev, globalByAction[action], schoolByAction[action])
		if buildErr != nil {
			return out, buildErr
		}
		out.Rules[action] = rule
	}
	if err = tx.Commit(); err != nil {
		return out, unavailable(err)
	}
	return out, nil
}

func checkReplay(ctx context.Context, tx *sql.Tx, actor, key, digest string, now time.Time) (e.Rule, bool, error) {
	var previous string
	var result []byte
	var at time.Time
	err := tx.QueryRowContext(ctx, `SELECT digest,result,created_at FROM platform.eligibility_requests WHERE actor=$1 AND key=$2`, actor, key).Scan(&previous, &result, &at)
	if err == nil && now.Before(at.Add(24*time.Hour)) {
		if previous != digest {
			return e.Rule{}, false, e.ErrConflict
		}
		var out e.Rule
		if json.Unmarshal(result, &out) != nil {
			return e.Rule{}, false, e.ErrUnavailable
		}
		return out, true, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return e.Rule{}, false, unavailable(err)
	}
	return e.Rule{}, false, nil
}

func lockSchoolRevision(ctx context.Context, tx *sql.Tx, school string) (int64, error) {
	var configRev int64
	if err := tx.QueryRowContext(ctx, `SELECT revision FROM platform.configuration WHERE singleton`).Scan(&configRev); err != nil || configRev < 1 {
		if err != nil {
			return 0, unavailable(err)
		}
		return 0, e.ErrUnavailable
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO platform.eligibility_revisions(school,revision) VALUES($1,1) ON CONFLICT(school) DO NOTHING`, school); err != nil {
		return 0, unavailable(err)
	}
	var revision int64
	if err := tx.QueryRowContext(ctx, `SELECT revision FROM platform.eligibility_revisions WHERE school=$1 FOR UPDATE`, school).Scan(&revision); err != nil {
		return 0, unavailable(err)
	}
	return revision, nil
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
	if _, err = tx.ExecContext(call, `SELECT pg_advisory_xact_lock(hashtextextended($1,726391840114))`, in.Actor+":"+in.Key); err != nil {
		return out, unavailable(err)
	}
	b, _ := json.Marshal([]any{in.School, in.Action, in.Change})
	hash := sha256.Sum256(b)
	digest := hex.EncodeToString(hash[:])
	if cached, hit, replayErr := checkReplay(call, tx, in.Actor, in.Key, digest, in.At); replayErr != nil || hit {
		return cached, replayErr
	}
	revision, err := lockSchoolRevision(call, tx, in.School)
	if err != nil {
		return out, err
	}
	if strconv.FormatInt(revision, 10) != in.Change.ExpectedVersion {
		return out, e.ErrConflict
	}
	conditions, _ := json.Marshal(in.Change.Conditions)
	var globalRaw []byte
	if in.School == "*" {
		globalRaw = conditions
	} else {
		err = tx.QueryRowContext(call, `SELECT conditions FROM platform.eligibility_rules WHERE school='*' AND action=$1`, in.Action).Scan(&globalRaw)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return out, unavailable(err)
		}
	}
	var schoolRaw []byte
	if in.School != "*" {
		schoolRaw = conditions
	}
	out, err = buildEffectiveRule(in.Action, in.School, revision+1, globalRaw, schoolRaw)
	if err != nil {
		return e.Rule{}, err
	}
	result, _ := json.Marshal(out)
	statements := []struct {
		query string
		args  []any
	}{
		{`DELETE FROM platform.eligibility_requests WHERE actor=$1 AND key=$2`, []any{in.Actor, in.Key}},
		{`UPDATE platform.eligibility_revisions SET revision=revision+1 WHERE school=$1`, []any{in.School}},
		{`INSERT INTO platform.eligibility_rules VALUES($1,$2,$3) ON CONFLICT(school,action) DO UPDATE SET conditions=EXCLUDED.conditions`, []any{in.School, in.Action, conditions}},
		{`INSERT INTO platform.eligibility_requests VALUES($1,$2,$3,$4,$5)`, []any{in.Actor, in.Key, digest, result, in.At}},
		{`INSERT INTO platform.audit(actor,action,target,data,created_at,school,module,reason) VALUES($1,'eligibility.set',$2,$3,$4,$5,'platform',$6)`, []any{in.Actor, in.School + ":" + in.Action, result, in.At, in.School, in.Change.Reason}},
	}
	if in.School == "*" {
		statements = append(statements, struct {
			query string
			args  []any
		}{`UPDATE platform.configuration SET revision=revision+1 WHERE singleton`, nil})
	}
	for _, statement := range statements {
		if _, err = tx.ExecContext(call, statement.query, statement.args...); err != nil {
			return e.Rule{}, unavailable(err)
		}
	}
	if err = tx.Commit(); err != nil {
		return e.Rule{}, unavailable(err)
	}
	return out, nil
}

func (r *Repository) Reset(ctx context.Context, in e.ResetUpdate) (e.Rule, error) {
	call, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out := e.Rule{}
	tx, err := r.DB.BeginTx(call, nil)
	if err != nil {
		return out, unavailable(err)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(call, `SELECT pg_advisory_xact_lock(hashtextextended($1,726391840114))`, in.Actor+":"+in.Key); err != nil {
		return out, unavailable(err)
	}
	b, _ := json.Marshal([]any{"reset", in.School, in.Action, in.Reset})
	hash := sha256.Sum256(b)
	digest := hex.EncodeToString(hash[:])
	if cached, hit, replayErr := checkReplay(call, tx, in.Actor, in.Key, digest, in.At); replayErr != nil || hit {
		return cached, replayErr
	}
	revision, err := lockSchoolRevision(call, tx, in.School)
	if err != nil {
		return out, err
	}
	if strconv.FormatInt(revision, 10) != in.Reset.ExpectedVersion {
		return out, e.ErrConflict
	}
	deleted, err := tx.ExecContext(call, `DELETE FROM platform.eligibility_rules WHERE school=$1 AND action=$2`, in.School, in.Action)
	if err != nil {
		return out, unavailable(err)
	}
	rows, err := deleted.RowsAffected()
	if err != nil {
		return out, unavailable(err)
	}
	if rows == 0 {
		return out, e.ErrConflict
	}
	var globalRaw []byte
	err = tx.QueryRowContext(call, `SELECT conditions FROM platform.eligibility_rules WHERE school='*' AND action=$1`, in.Action).Scan(&globalRaw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, unavailable(err)
	}
	out, err = buildEffectiveRule(in.Action, in.School, revision+1, globalRaw, nil)
	if err != nil {
		return e.Rule{}, err
	}
	result, _ := json.Marshal(out)
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`DELETE FROM platform.eligibility_requests WHERE actor=$1 AND key=$2`, []any{in.Actor, in.Key}},
		{`UPDATE platform.eligibility_revisions SET revision=revision+1 WHERE school=$1`, []any{in.School}},
		{`INSERT INTO platform.eligibility_requests VALUES($1,$2,$3,$4,$5)`, []any{in.Actor, in.Key, digest, result, in.At}},
		{`INSERT INTO platform.audit(actor,action,target,data,created_at,school,module,reason) VALUES($1,'eligibility.reset',$2,$3,$4,$5,'platform',$6)`, []any{in.Actor, in.School + ":" + in.Action, result, in.At, in.School, in.Reset.Reason}},
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
