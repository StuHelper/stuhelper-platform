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
	"os"
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
	if _, err = owner.Exec(`CREATE ROLE ` + role + ` LOGIN;REVOKE ALL ON SCHEMA public FROM PUBLIC;GRANT USAGE ON SCHEMA platform TO ` + role + `;GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA platform TO ` + role + `;GRANT USAGE ON ALL SEQUENCES IN SCHEMA platform TO ` + role + `;REVOKE UPDATE ON platform.audit,platform.eligibility_requests FROM ` + role + `;CREATE SCHEMA foreign_data;CREATE TABLE foreign_data.secret(value text);`); err != nil {
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
	if _, err = owner.Exec(`DROP TRIGGER fail ON platform.audit;INSERT INTO platform.audit(actor,action,target,data,created_at) SELECT 'admin','test','school','{}',$1 FROM generate_series(1,600)`, now.AddDate(-2, 0, 0)); err != nil {
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
