package postgres_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	e "github.com/StuHelper/stuhelper-platform/eligibility"
	h "github.com/StuHelper/stuhelper-platform/modulehistory"
	p "github.com/StuHelper/stuhelper-platform/postgres"
	_ "github.com/jackc/pgx/v5/stdlib"
	"log/slog"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

var ctx = context.Background()

func setup(t *testing.T) (*p.Repository, *sql.DB) {
	t.Helper()
	dsn := os.Getenv("STUHELPER_PLATFORM_TEST_DSN")
	if dsn == "" {
		t.Fatal("isolated PostgreSQL required")
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("platform_%d", time.Now().UnixNano())
	role := name + "_runtime"
	if _, err = admin.Exec(`CREATE DATABASE ` + name); err != nil {
		t.Fatal(err)
	}
	owner, err := sql.Open("pgx", dsn+" dbname="+name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		owner.Close()
		admin.Exec(`DROP DATABASE ` + name + ` WITH(FORCE)`)
		admin.Exec(`DROP ROLE ` + role)
		admin.Close()
	})
	for range 2 {
		if err = p.Migrate(ctx, owner); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = owner.Exec(`CREATE ROLE ` + role + ` LOGIN;REVOKE ALL ON SCHEMA public FROM PUBLIC;GRANT USAGE ON SCHEMA platform TO ` + role + `;GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA platform TO ` + role + `;GRANT USAGE ON ALL SEQUENCES IN SCHEMA platform TO ` + role + `;REVOKE UPDATE,DELETE ON platform.admin_roles FROM ` + role + `;REVOKE UPDATE ON platform.audit,platform.eligibility_requests,platform.admin_requests FROM ` + role + `;CREATE SCHEMA foreign_data;CREATE TABLE foreign_data.secret(value text);`); err != nil {
		t.Fatal(err)
	}
	runtime, err := sql.Open("pgx", dsn+" dbname="+name+" user="+role)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { runtime.Close() })
	repo := &p.Repository{DB: runtime}
	if err = repo.CheckRuntime(ctx); err != nil {
		t.Fatal(err)
	}
	if (&p.Repository{DB: owner}).CheckRuntime(ctx) == nil {
		t.Fatal("owner accepted")
	}
	if _, err = runtime.Exec(`SELECT * FROM foreign_data.secret`); err == nil {
		t.Fatal("foreign access allowed")
	}
	if _, err = runtime.Exec(`UPDATE platform.audit SET actor='forged'`); err == nil {
		t.Fatal("audit mutable")
	}
	return repo, owner
}
func TestConfigurationConcurrencyReplayRollbackAndRetention(t *testing.T) {
	repo, owner := setup(t)
	now := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
	s := e.Service{Repo: repo, Now: func() time.Time { return now }}
	rule, err := s.Get(ctx, "school", "content.enter")
	if err != nil || len(rule.Conditions) != 1 || rule.Conditions[0] != e.Content {
		t.Fatal(rule, err)
	}
	in := e.Change{ExpectedVersion: rule.Version, Conditions: []e.Condition{e.Phone}}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			r, err := s.Set(ctx, "admin", "same", "school", "content.enter", in)
			if err != nil || r.Version != "2" {
				t.Error(r, err)
			}
		})
	}
	wg.Wait()
	var n int
	if err = owner.QueryRow(`SELECT count(*) FROM platform.audit`).Scan(&n); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if _, err = s.Set(ctx, "admin", "same", "other", "content.enter", in); !errors.Is(err, e.ErrConflict) {
		t.Fatal(err)
	}
	if _, err = s.Set(ctx, "admin", "stale", "school", "content.enter", in); !errors.Is(err, e.ErrConflict) {
		t.Fatal(err)
	}
	if _, err = owner.Exec(`CREATE FUNCTION platform.fail_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test failure'; END $$;CREATE TRIGGER fail BEFORE INSERT ON platform.audit FOR EACH ROW EXECUTE FUNCTION platform.fail_audit()`); err != nil {
		t.Fatal(err)
	}
	in.ExpectedVersion = "2"
	if _, err = s.Set(ctx, "admin", "rollback", "school", "content.enter", in); !errors.Is(err, e.ErrUnavailable) {
		t.Fatal(err)
	}
	rule, _ = s.Get(ctx, "school", "content.enter")
	if rule.Version != "2" {
		t.Fatal("partial commit", rule)
	}
	if _, err = owner.Exec(`DROP TRIGGER fail ON platform.audit`); err != nil {
		t.Fatal(err)
	}
	if _, err = owner.Exec(`INSERT INTO platform.audit(actor,action,target,data,created_at) SELECT 'admin','test','school','{}',$1 FROM generate_series(1,600)`, now.AddDate(-2, 0, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Set(ctx, "admin", "above-cap", "school", "content.enter", in); err != nil {
		t.Fatal("audit volume blocked write", err)
	}
	n64, err := repo.Cleanup(ctx, now)
	if err != nil || n64 != 100 {
		t.Fatal(n64, err)
	}
	if err = owner.QueryRow(`SELECT count(*) FROM platform.audit WHERE action='eligibility.set'`).Scan(&n); err != nil || n != 2 {
		t.Fatal("live audit removed", n, err)
	}
	now = now.Add(25 * time.Hour)
	in.ExpectedVersion = "3"
	if _, err = s.Set(ctx, "admin", "same", "school", "content.enter", in); err != nil {
		t.Fatal("expired idempotency not released", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = s.Get(cancelled, "school", "content.enter"); err == nil {
		t.Fatal("cancellation ignored")
	}
}
func TestInstallationFactsAreDurableMonotoneAndUnknownFailsClosed(t *testing.T) {
	repo, owner := setup(t)
	if state, err := repo.State(ctx, "reviews"); err != nil || state != h.Unknown {
		t.Fatal(state, err)
	}
	if err := repo.Initialize(ctx, []string{"reviews", "resources"}); err != nil {
		t.Fatal(err)
	}
	if state, err := repo.State(ctx, "reviews"); err != nil || state != h.NeverInstalled {
		t.Fatal(state, err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if err := repo.ObserveInstalled(ctx, []string{"reviews"}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	restarted := &p.Repository{DB: repo.DB}
	if state, err := restarted.State(ctx, "reviews"); err != nil || state != h.Installed {
		t.Fatal(state, err)
	}
	if err := repo.Initialize(ctx, []string{"fresh", "reviews"}); err == nil {
		t.Fatal("installation reset allowed")
	}
	if state, err := repo.State(ctx, "fresh"); err != nil || state != h.Unknown {
		t.Fatal("partial initialization", state, err)
	}
	var n int
	if err := owner.QueryRow(`SELECT count(*) FROM platform.audit WHERE target='reviews'`).Scan(&n); err != nil || n != 2 {
		t.Fatal("duplicate installation audit", n, err)
	}
	if _, err := owner.Exec(`DROP TABLE platform.installations`); err != nil {
		t.Fatal(err)
	}
	if state, err := repo.State(ctx, "resources"); err == nil || state != h.Unknown {
		t.Fatal("failed history certified absence", state, err)
	}
}

func TestPurchaseRulesGlobalAndSchoolOverrides(t *testing.T) {
	repo, _ := setup(t)
	s := e.Service{Repo: repo}
	check := func(school, action string, want []e.Condition) e.Rule {
		t.Helper()
		rule, err := s.Get(ctx, school, action)
		if err != nil || !reflect.DeepEqual(rule.Conditions, want) {
			t.Fatal(school, action, rule, err)
		}
		return rule
	}
	for _, action := range []string{"resources.purchase", "membership.purchase"} {
		rule := check("school", action, []e.Condition{e.Campus})
		if _, err := s.Set(ctx, "admin", action+"-global", "*", action, e.Change{ExpectedVersion: rule.Version, Conditions: []e.Condition{e.Campus, e.Phone}}); err != nil {
			t.Fatal(err)
		}
		rule = check("school", action, []e.Condition{e.Campus, e.Phone})
		if _, err := s.Set(ctx, "admin", action+"-school", "school", action, e.Change{ExpectedVersion: rule.Version, Conditions: []e.Condition{}}); err != nil {
			t.Fatal(err)
		}
		check("school", action, []e.Condition{})
		check("other", action, []e.Condition{e.Campus, e.Phone})
		check("school", "resources.download.restricted", []e.Condition{e.Content})
		if action == "resources.purchase" {
			check("school", "membership.purchase", []e.Condition{e.Campus})
		}
	}
	if _, err := s.Get(ctx, "school", "resources.unlock"); !errors.Is(err, e.ErrRule) {
		t.Fatal("unknown action accepted", err)
	}
}

func TestRepositoryReadyCacheSingleflightRuntimePrivilegesAndDiagnostics(t *testing.T) {
	repo, owner := setup(t)
	var logs strings.Builder
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	repo.Now = func() time.Time { return now }
	repo.Logger = slog.New(slog.NewJSONHandler(&logs, nil))

	var runtimeRole string
	if err := repo.DB.QueryRow(`SELECT current_user`).Scan(&runtimeRole); err != nil {
		t.Fatal(err)
	}

	// A-14: missing required privileges on new tables or sequence must fail CheckRuntime and log redacted reason.
	for _, stmt := range [][2]string{
		{`REVOKE USAGE ON SEQUENCE platform.audit_snapshot_slot FROM ` + runtimeRole, `GRANT USAGE ON SEQUENCE platform.audit_snapshot_slot TO ` + runtimeRole},
		{`REVOKE INSERT ON platform.audit_snapshots FROM ` + runtimeRole, `GRANT INSERT ON platform.audit_snapshots TO ` + runtimeRole},
		{`REVOKE UPDATE ON platform.audit_retention FROM ` + runtimeRole, `GRANT UPDATE ON platform.audit_retention TO ` + runtimeRole},
		{`REVOKE SELECT ON platform.admin_roles FROM ` + runtimeRole, `GRANT SELECT ON platform.admin_roles TO ` + runtimeRole},
	} {
		if _, err := owner.Exec(stmt[0]); err != nil {
			t.Fatal(err)
		}
		if err := repo.CheckRuntime(ctx); !errors.Is(err, e.ErrUnavailable) {
			t.Fatalf("expected ErrUnavailable after %q, got %v", stmt[0], err)
		}
		if _, err := owner.Exec(stmt[1]); err != nil {
			t.Fatal(err)
		}
		if err := repo.CheckRuntime(ctx); err != nil {
			t.Fatalf("CheckRuntime did not recover after %q: %v", stmt[1], err)
		}
	}
	if !strings.Contains(logs.String(), `"reason":"db_privilege"`) {
		t.Fatalf("expected redacted db_privilege log, got %s", logs.String())
	}

	// A-06: Ready caches positive status for 1s and re-evaluates after TTL expiry.
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			if err := repo.Ready(ctx); err != nil {
				t.Errorf("concurrent Ready failed: %v", err)
			}
		})
	}
	wg.Wait()

	// Drop configuration table: within the 1s TTL window Ready uses memory snapshot, CheckRuntime fails immediately,
	// and after advancing past 1s TTL Ready fails closed and emits redacted sqlstate diagnostic.
	if _, err := owner.Exec(`DROP TABLE platform.configuration`); err != nil {
		t.Fatal(err)
	}
	if err := repo.Ready(ctx); err != nil {
		t.Fatalf("expected cached Ready within 1s window, got %v", err)
	}
	now = now.Add(2 * time.Second)
	if err := repo.Ready(ctx); !errors.Is(err, e.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable after Ready TTL expired, got %v", err)
	}
	if !strings.Contains(logs.String(), `"operation":"ready"`) || strings.Contains(logs.String(), "platform.configuration") {
		t.Fatalf("unexpected diagnostic log content: %s", logs.String())
	}
}

func TestEligibilityActionWhitelistAndMalformedInputsNeverReturn503(t *testing.T) {
	repo, owner := setup(t)
	s := e.Service{Repo: repo}

	// Unsupported course_enrollment on non-course-review actions returns ErrRule and writes nothing.
	for _, action := range []string{"content.enter", "resources.upload", "membership.purchase"} {
		if _, err := s.Set(ctx, "admin", "bad-"+action, "buaa", action, e.Change{ExpectedVersion: "1", Conditions: []e.Condition{e.Enrollment}}); !errors.Is(err, e.ErrRule) {
			t.Fatalf("%s: expected ErrRule, got %v", action, err)
		}
	}
	var count int
	if err := owner.QueryRow(`SELECT count(*) FROM platform.eligibility_rules`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rejected rules persisted: count=%d err=%v", count, err)
	}
	if err := owner.QueryRow(`SELECT count(*) FROM platform.audit`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rejected rules wrote audit: count=%d err=%v", count, err)
	}

	// Malformed UTF-8, NUL, whitespace and overlong school/key/actor/version return ErrRule, never ErrUnavailable.
	for _, badSchool := range []string{"", "   ", "buaa ", "\xff", "buaa\x00"} {
		if _, err := s.Get(ctx, badSchool, "content.enter"); !errors.Is(err, e.ErrRule) {
			t.Fatalf("Get(%q): expected ErrRule, got %v", badSchool, err)
		}
		if _, err := s.Set(ctx, "admin", "valid-key", badSchool, "content.enter", e.Change{ExpectedVersion: "1", Conditions: []e.Condition{}}); !errors.Is(err, e.ErrRule) {
			t.Fatalf("Set(school=%q): expected ErrRule, got %v", badSchool, err)
		}
	}
	if _, err := s.Set(ctx, "admin", "key\xff", "buaa", "content.enter", e.Change{ExpectedVersion: "1", Conditions: []e.Condition{}}); !errors.Is(err, e.ErrRule) {
		t.Fatalf("Set(bad key): expected ErrRule, got %v", err)
	}
	if _, err := s.Set(ctx, "admin\xff", "valid-key", "buaa", "content.enter", e.Change{ExpectedVersion: "1", Conditions: []e.Condition{}}); !errors.Is(err, e.ErrRule) {
		t.Fatalf("Set(bad actor): expected ErrRule, got %v", err)
	}

	// Supported course_enrollment on publish_course persists rule and audit atomically.
	rule, err := s.Set(ctx, "admin", "ok-course", "buaa", "publish_course", e.Change{ExpectedVersion: "1", Conditions: []e.Condition{e.Campus, e.Enrollment}})
	if err != nil || rule.Version != "2" || len(rule.SupportedConditions) != 5 {
		t.Fatalf("publish_course Set failed: %+v, %v", rule, err)
	}
	if err := owner.QueryRow(`SELECT count(*) FROM platform.eligibility_rules WHERE school='buaa' AND action='publish_course'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("expected 1 persisted rule, got %d (%v)", count, err)
	}

	// Corrupted unsupported condition in database fails closed with ErrUnavailable.
	if _, err := owner.Exec(`INSERT INTO platform.eligibility_rules(school,action,conditions) VALUES('buaa','content.enter','["course_enrollment"]')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "buaa", "content.enter"); !errors.Is(err, e.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable for corrupted DB condition, got %v", err)
	}
}
