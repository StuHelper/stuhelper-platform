package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/StuHelper/stuhelper-platform/account"
	a "github.com/StuHelper/stuhelper-platform/admin"
	ah "github.com/StuHelper/stuhelper-platform/admin/httpapi"
	e "github.com/StuHelper/stuhelper-platform/eligibility"
	p "github.com/StuHelper/stuhelper-platform/postgres"
)

var boss = account.Principal{Issuer: "urn:test", Subject: "boss"}
var delegate = account.Principal{Issuer: "urn:test", Subject: "delegate"}
var member = account.Principal{Issuer: "urn:test", Subject: "member"}
var school = a.Scope{School: "school-a", Group: "*", Module: "resources"}

type identity struct {
	unavailable atomic.Bool
	revoked     atomic.Bool
}

func (i *identity) PlatformAdmin(_ context.Context, p account.Principal) (bool, error) {
	if i.unavailable.Load() {
		return false, a.ErrUnavailable
	}
	if i.revoked.Load() {
		return false, a.ErrForbidden
	}
	return p == boss, nil
}
func service(t *testing.T) (a.Service, *p.Repository) {
	t.Helper()
	repo, _ := setup(t)
	return a.Service{Repo: repo, Identity: &identity{}}, repo
}
func role(t *testing.T, s a.Service, id string, caps ...string) {
	t.Helper()
	_, err := s.CreateRole(ctx, boss, "create:"+id, a.RoleInput{Role: a.Role{ID: id, Name: id, Capabilities: caps}, Reason: "synthetic test"})
	if err != nil {
		t.Fatal(err)
	}
}
func grant(t *testing.T, s a.Service, who account.Principal, id, revision string, scope a.Scope) string {
	t.Helper()
	r, e := s.Change(ctx, boss, who.Key(), fmt.Sprintf("%s:%s:%s", who.Subject, id, revision), a.Mutation{RoleID: id, Scope: scope, ExpectedRevision: revision, Reason: "synthetic grant"}, false)
	if e != nil {
		t.Fatal(e)
	}
	return r.Revision
}
func deny(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, a.ErrForbidden) {
		t.Fatalf("want forbidden, got %v", err)
	}
}
func TestAdminAO01AO04ScopeAndSeparateCapabilities(t *testing.T) {
	s, _ := service(t)
	role(t, s, "groups", "groups.manage")
	role(t, s, "query", "billing.accounts.read", "verification.profile.read")
	groupScope := a.Scope{School: "school-a", Group: "group-a", Module: "groups"}
	grant(t, s, member, "groups", "0", groupScope)
	if err := s.Evaluate(ctx, member, "groups.manage", groupScope); err != nil {
		t.Fatal(err)
	}
	deny(t, s.Evaluate(ctx, member, "resources.manage", school))
	deny(t, s.Evaluate(ctx, member, "billing.refund", groupScope))
	outside := groupScope
	outside.Group = "group-b"
	deny(t, s.Evaluate(ctx, member, "groups.manage", outside))
	outside = groupScope
	outside.School = "school-b"
	deny(t, s.Evaluate(ctx, member, "groups.manage", outside))
	outside = groupScope
	outside.Module = "billing"
	deny(t, s.Evaluate(ctx, member, "groups.manage", outside))
	grant(t, s, member, "query", "1", a.Global)
	if err := s.Evaluate(ctx, member, "billing.accounts.read", a.Global); err != nil {
		t.Fatal(err)
	}
	deny(t, s.Evaluate(ctx, member, "billing.refund", a.Global))
	deny(t, s.Evaluate(ctx, member, "verification.modify", a.Global))
	// Trusted platform identity includes sensitive business capabilities.
	if err := s.Evaluate(ctx, boss, "billing.refund", a.Global); err != nil {
		t.Fatal(err)
	}
}
func TestAdminDelegationSelfEscalationAndImmediateRevocation(t *testing.T) {
	s, repo := service(t)
	role(t, s, "delegation", "platform.authorization.manage", "platform.authorization.read", "platform.audit.read", "resources.manage")
	role(t, s, "resources", "resources.manage")
	role(t, s, "refund", "billing.refund")
	role(t, s, "platform", "platform.modules")
	grant(t, s, delegate, "delegation", "0", school)
	input := a.Mutation{RoleID: "resources", Scope: school, ExpectedRevision: "0", Reason: "delegate"}
	_, err := s.Change(ctx, delegate, delegate.Key(), "self", input, false)
	deny(t, err)
	_, err = s.Change(ctx, boss, boss.Key(), "self-admin", input, false)
	deny(t, err)
	input.Scope.School = "school-b"
	_, err = s.Change(ctx, delegate, member.Key(), "outside", input, false)
	deny(t, err)
	input.Scope = school
	input.RoleID = "refund"
	_, err = s.Change(ctx, delegate, member.Key(), "refund", input, false)
	deny(t, err)
	input.RoleID = "platform"
	_, err = s.Change(ctx, delegate, member.Key(), "platform", input, false)
	deny(t, err)
	input.RoleID = "resources"
	out, err := s.Change(ctx, delegate, member.Key(), "legitimate", input, false)
	if err != nil || out.Revision != "1" {
		t.Fatal(out, err)
	}
	if err = s.Evaluate(ctx, member, "resources.manage", school); err != nil {
		t.Fatal(err)
	}
	// A new service instance observes the same durable grants without a cache.
	restarted := a.Service{Repo: &p.Repository{DB: repo.DB}, Identity: s.Identity}
	input.ExpectedRevision = "1"
	out, err = s.Change(ctx, delegate, member.Key(), "revoke", input, true)
	if err != nil || out.Revision != "2" {
		t.Fatal(out, err)
	}
	deny(t, restarted.Evaluate(ctx, member, "resources.manage", school))
	effective, err := s.Effective(ctx, member, member.Key(), a.Global)
	if err != nil || effective.Revision != "2" || len(effective.Grants) != 0 {
		t.Fatal(effective, err)
	}
	// Removing the delegator prevents retry receipts from bypassing current permission.
	revoke := a.Mutation{RoleID: "delegation", Scope: school, ExpectedRevision: "1", Reason: "remove delegator"}
	if _, err = s.Change(ctx, boss, delegate.Key(), "remove", revoke, true); err != nil {
		t.Fatal(err)
	}
	input.ExpectedRevision = "0"
	_, err = s.Change(ctx, delegate, member.Key(), "legitimate", input, false)
	deny(t, err)
	var count int
	if err = repo.DB.QueryRow(`SELECT count(*) FROM platform.admin_grants`).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
}
func TestAdminAO02ConcurrentRevisionUniquenessAndReplay(t *testing.T) {
	s, repo := service(t)
	role(t, s, "resources", "resources.manage")
	in := a.Mutation{RoleID: "resources", Scope: school, ExpectedRevision: "0", Reason: "concurrent"}
	var wg sync.WaitGroup
	var success atomic.Int32
	for i := range 8 {
		wg.Go(func() {
			_, err := s.Change(ctx, boss, member.Key(), fmt.Sprint("concurrent:", i), in, false)
			if err == nil {
				success.Add(1)
			} else if !errors.Is(err, a.ErrConflict) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if success.Load() != 1 {
		t.Fatal(success.Load())
	}
	var count int
	if err := repo.DB.QueryRow(`SELECT count(*) FROM platform.admin_grants`).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	if err := repo.DB.QueryRow(`SELECT count(*) FROM platform.audit WHERE action='role.grant'`).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	if _, err := repo.DB.Exec(`INSERT INTO platform.admin_grants SELECT * FROM platform.admin_grants`); err == nil {
		t.Fatal("database uniqueness missing")
	}
	in.ExpectedRevision = "1"
	for range 8 {
		wg.Go(func() {
			r, err := s.Change(ctx, boss, member.Key(), "same-revoke", in, true)
			if err != nil || r.Revision != "2" {
				t.Error(r, err)
			}
		})
	}
	wg.Wait()
	if err := repo.DB.QueryRow(`SELECT count(*) FROM platform.audit WHERE action='role.revoke'`).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	in.Reason = "different request"
	if _, err := s.Change(ctx, boss, member.Key(), "same-revoke", in, true); !errors.Is(err, a.ErrConflict) {
		t.Fatal(err)
	}
}
func TestAdminAtomicAuditFailureAndDatabaseIsolation(t *testing.T) {
	repo, owner := setup(t)
	s := a.Service{Repo: repo, Identity: &identity{}}
	role(t, s, "resources", "resources.manage")
	if _, err := repo.DB.Exec(`UPDATE platform.admin_roles SET definition='{}'`); err == nil {
		t.Fatal("mutable role definition")
	}
	if _, err := repo.DB.Exec(`DELETE FROM platform.admin_roles`); err == nil {
		t.Fatal("removable role definition")
	}
	if _, err := repo.DB.Exec(`UPDATE platform.admin_requests SET result='{}'`); err == nil {
		t.Fatal("mutable receipts")
	}
	_, err := owner.Exec(`CREATE FUNCTION platform.fail_admin_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic audit failure'; END $$; CREATE TRIGGER fail_admin BEFORE INSERT ON platform.audit FOR EACH ROW EXECUTE FUNCTION platform.fail_admin_audit()`)
	if err != nil {
		t.Fatal(err)
	}
	in := a.Mutation{RoleID: "resources", Scope: school, ExpectedRevision: "0", Reason: "atomic"}
	if _, err = s.Change(ctx, boss, member.Key(), "rollback", in, false); !errors.Is(err, a.ErrUnavailable) {
		t.Fatal(err)
	}
	current, err := s.Effective(ctx, boss, member.Key(), a.Global)
	if err != nil || current.Revision != "0" || len(current.Grants) != 0 {
		t.Fatal(current, err)
	}
	var count int
	if err = owner.QueryRow(`SELECT count(*) FROM platform.admin_requests WHERE key='rollback'`).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
	if _, err = owner.Exec(`DROP TRIGGER fail_admin ON platform.audit`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Change(ctx, boss, member.Key(), "rollback", in, false); err != nil {
		t.Fatal(err)
	}
	s.Identity.(*identity).unavailable.Store(true)
	if err = s.Evaluate(ctx, member, "resources.manage", school); !errors.Is(err, a.ErrUnavailable) {
		t.Fatal(err)
	}
	s.Identity.(*identity).unavailable.Store(false)
	s.Identity.(*identity).revoked.Store(true)
	deny(t, s.Evaluate(ctx, member, "resources.manage", school))
}
func TestPlatformOperationsScopeFiltersCursorAndRevocation(t *testing.T) {
	s, repo := service(t)
	role(t, s, "audit", "platform.audit.read", "platform.authorization.read")
	role(t, s, "resources", "resources.manage")
	grant(t, s, delegate, "audit", "0", school)
	grant(t, s, member, "resources", "0", school)
	f := a.Filter{Scope: school, Limit: 1, Action: "role.grant"}
	page, err := s.Operations(ctx, delegate, f)
	if err != nil || page.Source != "platform" || len(page.Items) != 1 || page.NextCursor == "" || page.Items[0].RoleID != "resources" || page.Items[0].Revision != "1" {
		t.Fatal(page, err)
	}
	f.Cursor = page.NextCursor
	page, err = s.Operations(ctx, delegate, f)
	if err != nil || len(page.Items) != 1 || page.NextCursor != "" {
		t.Fatal(page, err)
	}
	changed := f
	changed.Action = "role.revoke"
	if _, err = s.Operations(ctx, delegate, changed); !errors.Is(err, a.ErrInvalid) {
		t.Fatal(err)
	}
	outside := a.Filter{Scope: a.Scope{School: "school-b", Group: "*", Module: "resources"}, Limit: 100}
	_, err = s.Operations(ctx, delegate, outside)
	deny(t, err)
	_, err = s.Effective(ctx, delegate, member.Key(), outside.Scope)
	deny(t, err)
	_, err = s.Effective(ctx, delegate, member.Key(), a.Global)
	deny(t, err)
	now := time.Now().UTC()
	filter := a.Filter{Scope: school, Limit: 100, Actor: boss.Key(), Target: member.Key(), Action: "role.grant", From: now.Add(-time.Hour), Until: now.Add(time.Hour)}
	page, err = s.Operations(ctx, delegate, filter)
	if err != nil || len(page.Items) != 1 {
		t.Fatal(page, err)
	}
	filter.Limit = 101
	if _, err = s.Operations(ctx, delegate, filter); !errors.Is(err, a.ErrInvalid) {
		t.Fatal(err)
	}
	// Historical audit lacking scope cannot be read using a scoped permission.
	if _, err = repo.DB.Exec(`INSERT INTO platform.audit(actor,action,target,data,created_at) VALUES('historical','legacy','target','{}',now())`); err != nil {
		t.Fatal(err)
	}
	page, err = s.Operations(ctx, delegate, a.Filter{Scope: school, Limit: 100, Action: "legacy"})
	if err != nil || len(page.Items) != 0 {
		t.Fatal(page, err)
	}
	page, err = s.Operations(ctx, boss, a.Filter{Scope: a.Global, Limit: 100, Action: "legacy"})
	if err != nil || len(page.Items) != 1 {
		t.Fatal(page, err)
	}
	in := a.Mutation{RoleID: "audit", Scope: school, ExpectedRevision: "1", Reason: "revoke audit"}
	if _, err = s.Change(ctx, boss, delegate.Key(), "revoke-audit", in, true); err != nil {
		t.Fatal(err)
	}
	_, err = s.Operations(ctx, delegate, f)
	deny(t, err)
	// Existing platform producers are queryable without cross-module database access.
	if _, err = (e.Service{Repo: repo}).Set(ctx, boss.Key(), "eligibility", "school-a", "content.enter", e.Change{ExpectedVersion: "1", Conditions: []e.Condition{e.Phone}}); err != nil {
		t.Fatal(err)
	}
	if err = repo.ObserveInstalled(ctx, []string{"reviews"}); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"eligibility.set", "installation.record"} {
		page, err = s.Operations(ctx, boss, a.Filter{Scope: a.Global, Limit: 100, Action: action})
		if err != nil || len(page.Items) != 1 || page.Items[0].Source != "platform" {
			t.Fatal(page, err)
		}
	}
}
func TestAdminHTTPAuthenticationValidationAndConflict(t *testing.T) {
	s, _ := service(t)
	role(t, s, "resources", "resources.manage")
	mux := http.NewServeMux()
	ah.Handler{Service: s, Authenticate: func(r *http.Request) (account.Principal, error) {
		if r.Header.Get("Authorization") != "synthetic" {
			return account.Principal{}, a.ErrUnauthenticated
		}
		return boss, nil
	}}.Register(mux)
	path := "/api/v1/platform/admin/administrators/" + url.PathEscape(member.Key()) + "/grants"
	body := `{"role_id":"resources","scope":{"school":"school-a","group":"*","module":"resources"},"expected_revision":"0","reason":"HTTP"}`
	call := func(method, path, body, key, auth string, want int) {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", auth)
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
	}
	call("PUT", path, body, "http", "", 401)
	call("PUT", path, body, "http", "synthetic", 200)
	call("PUT", path, body, "http", "synthetic", 200)
	call("PUT", path, body, "stale", "synthetic", 409)
	call("PUT", path, strings.Replace(body, `"reason":"HTTP"`, `"reason":"HTTP","platform_admin":true`, 1), "invalid", "synthetic", 400)
	for _, q := range []string{"limit=101", "limit=0", "limit=x", "from=bad", "school=a&school=b", "unexpected=x", "cursor=garbage"} {
		call("GET", "/api/v1/platform/admin/operations?"+q, "", "", "synthetic", 400)
	}
	call("GET", "/api/v1/platform/admin/role-templates", "", "", "synthetic", 200)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := s.Evaluate(cancelled, member, "resources.manage", school); err == nil {
		t.Fatal("cancellation ignored")
	}
}

func TestAdminMigrationRetentionAndPageBound(t *testing.T) {
	repo, owner := setup(t)
	// Reconstruct the pre-admin schema in this test's disposable database while
	// keeping an existing configuration audit, then exercise the actual migration.
	if _, err := owner.Exec(`INSERT INTO platform.audit(actor,action,target,data,created_at) VALUES('old','eligibility.set','school:content.enter','{}',now());
 DROP TABLE platform.admin_grants,platform.admin_subjects,platform.admin_roles,platform.admin_requests;
 ALTER TABLE platform.audit DROP COLUMN school,DROP COLUMN group_id,DROP COLUMN module,DROP COLUMN reason,DROP COLUMN result`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := p.Migrate(ctx, owner); err != nil {
			t.Fatal(err)
		}
	}
	var scope, reason string
	if err := owner.QueryRow(`SELECT school,reason FROM platform.audit WHERE actor='old'`).Scan(&scope, &reason); err != nil || scope != "*" || reason != "" {
		t.Fatal(scope, reason, err)
	}
	var runtime string
	if err := repo.DB.QueryRow(`SELECT current_user`).Scan(&runtime); err != nil {
		t.Fatal(err)
	}
	// runtime is generated by setup from a fixed prefix and decimal timestamp.
	if _, err := owner.Exec(`GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA platform TO ` + runtime + `;REVOKE UPDATE,DELETE ON platform.admin_roles FROM ` + runtime + `;REVOKE UPDATE ON platform.admin_requests,platform.audit,platform.eligibility_requests FROM ` + runtime); err != nil {
		t.Fatal(err)
	}
	if err := repo.CheckRuntime(ctx); err != nil {
		t.Fatal(err)
	}
	s := a.Service{Repo: repo, Identity: &identity{}}
	if _, err := owner.Exec(`INSERT INTO platform.audit(actor,action,target,data,created_at) SELECT 'test','page','object','{}',now() FROM generate_series(1,103)`); err != nil {
		t.Fatal(err)
	}
	f := a.Filter{Scope: a.Global, Action: "page", Limit: 100}
	page, err := s.Operations(ctx, boss, f)
	if err != nil || len(page.Items) != 100 || page.NextCursor == "" {
		t.Fatal(len(page.Items), err)
	}
	firstLast := page.Items[99].ID
	f.Cursor = page.NextCursor
	page, err = s.Operations(ctx, boss, f)
	if err != nil || len(page.Items) != 3 || page.NextCursor != "" || page.Items[0].ID == firstLast {
		t.Fatal(page, err)
	}
	now := time.Now().UTC()
	if _, err = owner.Exec(`INSERT INTO platform.admin_requests(actor,key,digest,result,created_at) SELECT 'test',n::text,'digest','{}',$1 FROM generate_series(1,150) n`, now.Add(-25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err = owner.Exec(`INSERT INTO platform.admin_requests VALUES('test','live','digest','{}',$1)`, now); err != nil {
		t.Fatal(err)
	}
	n, err := repo.Cleanup(ctx, now)
	if err != nil || n != 100 {
		t.Fatal(n, err)
	}
	var count int
	if err = owner.QueryRow(`SELECT count(*) FROM platform.admin_requests`).Scan(&count); err != nil || count != 51 {
		t.Fatal(count, err)
	}
	role(t, s, "immutable", "resources.manage")
	_, err = s.CreateRole(ctx, boss, "redefine", a.RoleInput{Role: a.Role{ID: "immutable", Name: "changed", Capabilities: []string{"billing.refund"}}, Reason: "redefine"})
	if !errors.Is(err, a.ErrConflict) {
		t.Fatal(err)
	}
	current, err := s.Role(ctx, boss, "immutable")
	if err != nil || len(current.Capabilities) != 1 || current.Capabilities[0] != "resources.manage" {
		t.Fatal(current, err)
	}
}
