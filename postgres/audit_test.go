package postgres_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/StuHelper/stuhelper-platform/account"
	ah "github.com/StuHelper/stuhelper-platform/admin/httpapi"
	sdk "github.com/StuHelper/stuhelper-platform/sdk/admin"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	a "github.com/StuHelper/stuhelper-platform/admin"
)

// This adapter models a module's immutable snapshot membership and SQL filters.
// The integration service still uses real PostgreSQL for current authorization.
type auditProvider struct {
	rows      []a.Record
	snapshots map[string][]a.Record
	calls     []a.AuditRequest
	fail      bool
	invalid   bool
	timeout   bool
}

func (p *auditProvider) ReadAudit(ctx context.Context, q a.AuditRequest) (a.AuditBatch, error) {
	p.calls = append(p.calls, q)
	if p.fail {
		return a.AuditBatch{}, errors.New("synthetic provider failure")
	}
	if p.timeout {
		<-ctx.Done()
		return a.AuditBatch{}, ctx.Err()
	}
	if p.snapshots == nil {
		p.snapshots = map[string][]a.Record{}
	}
	token := q.Snapshot
	if token == "" {
		token = strconv.Itoa(len(p.snapshots) + 1)
		p.snapshots[token] = slices.Clone(p.rows)
	}
	rows, ok := p.snapshots[token]
	if !ok {
		return a.AuditBatch{}, a.ErrUnavailable
	}
	out := a.AuditBatch{Snapshot: token, Records: []a.Record{}}
	if p.invalid {
		return a.AuditBatch{Snapshot: token, Records: slices.Clone(rows)}, nil
	}
	for _, r := range rows {
		permitted := false
		for _, scope := range q.Scopes {
			if scope.Contains(r.Scope) {
				permitted = true
			}
		}
		if !permitted || r.CreatedAt.Before(q.From) || !r.CreatedAt.Before(q.Until) || (q.Actor != "" && q.Actor != r.Actor) || (q.Target != "" && q.Target != r.Target) || (q.Action != "" && q.Action != r.Action) {
			continue
		}
		if q.Before != nil && auditCompare(r, a.Record{CreatedAt: q.Before.Time, Source: q.Before.Source, ID: q.Before.ID}) >= 0 {
			continue
		}
		out.Records = append(out.Records, r)
	}
	slices.SortFunc(out.Records, func(x, y a.Record) int { return -auditCompare(x, y) })
	if len(out.Records) > q.Limit {
		out.Records = out.Records[:q.Limit]
	}
	return out, nil
}
func auditCompare(x, y a.Record) int {
	if n := x.CreatedAt.Compare(y.CreatedAt); n != 0 {
		return n
	}
	if n := strings.Compare(x.Source, y.Source); n != 0 {
		return n
	}
	return strings.Compare(x.ID, y.ID)
}
func auditRow(source, id, school string, at time.Time) a.Record {
	return a.Record{ID: id, Source: source, Actor: "synthetic", Target: "object", Action: "review", Scope: a.Scope{School: school, Group: "*", Module: source}, Reason: "synthetic reason", Result: "success", CreatedAt: at}
}
func TestAuditProvidersStableMergeAndSnapshot(t *testing.T) {
	s, _ := service(t)
	now := time.Now().UTC()
	s.Now = func() time.Time { return now }
	r := &auditProvider{rows: []a.Record{auditRow("resources", "b", "a", now.Add(-time.Minute)), auditRow("resources", "a", "a", now.Add(-time.Minute))}}
	v := &auditProvider{rows: []a.Record{auditRow("verification", "b", "a", now.Add(-time.Minute)), auditRow("verification", "a", "a", now.Add(-2*time.Minute))}}
	s.AuditProviders = map[string]a.AuditProvider{"resources": r, "verification": v}
	f := a.Filter{Scope: a.Global, Sources: []string{"resources", "verification"}, Limit: 1}
	keys := []string{}
	for i := 0; i < 5; i++ {
		page, err := s.Operations(ctx, boss, f)
		if err != nil || page.Partial || len(page.Items) != 1 {
			t.Fatal(page, err)
		}
		keys = append(keys, page.Items[0].Source+":"+page.Items[0].ID)
		if i == 0 {
			r.rows = append(r.rows, auditRow("resources", "new", "a", now.Add(-90*time.Second)))
		}
		if page.NextCursor == "" {
			break
		}
		f.Cursor = page.NextCursor
	}
	want := []string{"verification:b", "resources:b", "resources:a", "verification:a"}
	if !slices.Equal(keys, want) {
		t.Fatal(keys)
	}
	if len(r.calls) != 4 || r.calls[1].Snapshot == "" || r.calls[1].Limit != 2 {
		t.Fatal(r.calls)
	}
	f.Cursor = ""
	f.Sources = []string{"resources"}
	f.Limit = 100
	page, err := s.Operations(ctx, boss, f)
	if err != nil || len(page.Items) != 3 {
		t.Fatal(page, err)
	}
}
func TestAuditProvidersAuthorizationBeforeReadAndRevokedPage(t *testing.T) {
	s, _ := service(t)
	now := time.Now().UTC()
	r := &auditProvider{rows: []a.Record{auditRow("resources", "outside", "b", now.Add(-time.Minute)), auditRow("resources", "2", "a", now.Add(-2*time.Minute)), auditRow("resources", "1", "a", now.Add(-3*time.Minute))}}
	v := &auditProvider{rows: []a.Record{auditRow("verification", "private", "b", now.Add(-time.Minute))}}
	s.AuditProviders = map[string]a.AuditProvider{"resources": r, "verification": v}
	role(t, s, "read-audit", "platform.audit.read")
	limited := a.Scope{School: "a", Group: "*", Module: "resources"}
	grant(t, s, delegate, "read-audit", "0", limited)
	f := a.Filter{Scope: a.Global, Limit: 1, Sources: []string{"resources"}, Actor: "synthetic", Target: "object", Action: "review"}
	page, err := s.Operations(ctx, delegate, f)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != "2" {
		t.Fatal(page, err)
	}
	if len(r.calls) != 1 || len(r.calls[0].Scopes) != 1 || r.calls[0].Scopes[0] != limited || len(v.calls) != 0 {
		t.Fatal("unrestricted provider call", r.calls, v.calls)
	}
	denied := f
	denied.Sources = []string{"verification"}
	_, err = s.Operations(ctx, delegate, denied)
	deny(t, err)
	if len(v.calls) != 0 {
		t.Fatal("unauthorized source called")
	}
	_, err = s.Change(ctx, boss, delegate.Key(), "remove-provider-read", a.Mutation{RoleID: "read-audit", Scope: limited, ExpectedRevision: "1", Reason: "revoke"}, true)
	if err != nil {
		t.Fatal(err)
	}
	f.Cursor = page.NextCursor
	_, err = s.Operations(ctx, delegate, f)
	deny(t, err)
	if len(r.calls) != 1 {
		t.Fatal("provider called after revocation")
	}
}
func TestAuditPartialResultsStayMarkedAndRejectBadProvider(t *testing.T) {
	s, _ := service(t)
	now := time.Now().UTC()
	r := &auditProvider{rows: []a.Record{auditRow("resources", "2", "a", now.Add(-time.Minute)), auditRow("resources", "1", "a", now.Add(-time.Minute))}}
	v := &auditProvider{fail: true}
	s.AuditProviders = map[string]a.AuditProvider{"resources": r, "verification": v}
	f := a.Filter{Scope: a.Global, Sources: []string{"resources", "verification"}, Limit: 1}
	page, err := s.Operations(ctx, boss, f)
	if err != nil || !page.Partial || !slices.Equal(page.MissingModules, []string{"verification"}) || page.NextCursor == "" {
		t.Fatal(page, err)
	}
	v.fail = false
	v.rows = []a.Record{auditRow("verification", "new", "a", now.Add(-time.Minute))}
	f.Cursor = page.NextCursor
	page, err = s.Operations(ctx, boss, f)
	if err != nil || !page.Partial || len(page.Items) != 1 || page.Items[0].Source != "resources" || len(v.calls) != 1 {
		t.Fatal(page, err)
	}
	f.Cursor = ""
	f.Limit = 100
	page, err = s.Operations(ctx, boss, f)
	if err != nil || page.Partial || len(page.Items) != 3 {
		t.Fatal(page, err)
	}
	// A provider returning a foreign scope loses its whole batch, not just a row.
	r.invalid = true
	r.rows = []a.Record{auditRow("resources", "bad", "b", now.Add(-time.Minute))}
	f.Scope = a.Scope{School: "a", Group: "*", Module: "*"}
	page, err = s.Operations(ctx, boss, f)
	if err != nil || !page.Partial || !slices.Contains(page.MissingModules, "resources") {
		t.Fatal(page, err)
	}
	for _, row := range page.Items {
		if row.Scope.School != "a" {
			t.Fatal("scope leak")
		}
	}
	v.timeout = true
	r.invalid = false
	page, err = s.Operations(ctx, boss, f)
	if err != nil || !page.Partial || !slices.Contains(page.MissingModules, "verification") {
		t.Fatal(page, err)
	}
	s.AuditProviders["verification"] = nil
	page, err = s.Operations(ctx, boss, f)
	if err != nil || !page.Partial {
		t.Fatal(page, err)
	}
}
func TestPlatformAuditSnapshotExcludesDelayedCommitAndDetectsRetention(t *testing.T) {
	s, repo := service(t)
	now := time.Now().UTC()
	tx, err := repo.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	// Allocate a lower sequence ID without committing until after page one.
	if _, err = tx.ExecContext(ctx, `INSERT INTO platform.audit(actor,action,target,data,created_at) VALUES('synthetic','snapshot','delayed','{}',$1)`, now.Add(-2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if _, err = repo.DB.Exec(`INSERT INTO platform.audit(actor,action,target,data,created_at) VALUES('synthetic','snapshot',$1,'{}',$2)`, fmt.Sprint(i), now.Add(-time.Duration(i+1)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	f := a.Filter{Scope: a.Global, Action: "snapshot", Limit: 1}
	page, err := s.Operations(ctx, boss, f)
	if err != nil || page.Partial || len(page.Items) != 1 {
		t.Fatal(page, err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	f.Cursor = page.NextCursor
	count := 1
	for f.Cursor != "" {
		page, err = s.Operations(ctx, boss, f)
		if err != nil || page.Partial {
			t.Fatal(page, err)
		}
		for _, r := range page.Items {
			if r.Target == "delayed" {
				t.Fatal("delayed commit entered snapshot")
			}
			count++
		}
		f.Cursor = page.NextCursor
	}
	if count != 3 {
		t.Fatal(count)
	}
	f.Cursor = ""
	page, err = s.Operations(ctx, boss, f)
	if err != nil {
		t.Fatal(err)
	}
	f.Cursor = page.NextCursor
	if _, err = repo.DB.Exec(`INSERT INTO platform.audit(actor,action,target,data,created_at) VALUES('synthetic','old','object','{}',$1)`, now.AddDate(-2, 0, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.Cleanup(ctx, now); err != nil {
		t.Fatal(err)
	}
	page, err = s.Operations(ctx, boss, f)
	if err != nil || !page.Partial || !slices.Equal(page.MissingModules, []string{"platform"}) {
		t.Fatal(page, err)
	}
}

func TestAuditHTTPGeneratedClientPartialResponse(t *testing.T) {
	s, _ := service(t)
	rows := &auditProvider{rows: []a.Record{auditRow("resources", "one", "a", time.Now().Add(-time.Minute))}}
	s.AuditProviders = map[string]a.AuditProvider{"resources": rows, "verification": nil}
	mux := http.NewServeMux()
	ah.Handler{Service: s, Authenticate: func(*http.Request) (account.Principal, error) { return boss, nil }}.Register(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	client, err := sdk.NewClientWithResponses(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	sources := []string{"resources", "verification"}
	response, err := client.ListPlatformOperationsWithResponse(ctx, &sdk.ListPlatformOperationsParams{Sources: &sources})
	if err != nil || response.JSON200 == nil {
		t.Fatal(response, err)
	}
	body := response.JSON200
	if !body.Partial || !slices.Equal(body.MissingModules, []string{"verification"}) || len(body.Items) != 1 || body.Items[0].Source != "resources" || len(body.QueriedModules) != 2 {
		t.Fatal(body)
	}
	sources = []string{"unknown"}
	response, err = client.ListPlatformOperationsWithResponse(ctx, &sdk.ListPlatformOperationsParams{Sources: &sources})
	if err != nil || response.JSON400 == nil {
		t.Fatal(response, err)
	}
}

func TestPlatformAuditManyPendingTransactions(t *testing.T) {
	s, repo := service(t)
	now := time.Now().UTC()
	var pending []*sql.Tx
	defer func() {
		for _, tx := range pending {
			_ = tx.Rollback()
		}
	}()
	for range 70 {
		tx, err := repo.DB.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		pending = append(pending, tx)
		if _, err = tx.Exec(`INSERT INTO platform.audit(actor,action,target,data,created_at) VALUES('synthetic','many-pending','delayed','{}',$1)`, now.Add(-time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 3 {
		if _, err := repo.DB.Exec(`INSERT INTO platform.audit(actor,action,target,data,created_at) VALUES('synthetic','many-pending',$1,'{}',$2)`, fmt.Sprint(i), now.Add(-time.Duration(i+2)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	var raw string
	if err := repo.DB.QueryRow(`SELECT pg_current_snapshot()::text`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if len(raw) <= 256 {
		t.Fatalf("fixture snapshot too small: %d", len(raw))
	}
	f := a.Filter{Scope: a.Global, Action: "many-pending", Limit: 1}
	page, err := s.Operations(ctx, boss, f)
	if err != nil || page.Partial || len(page.Items) != 1 || page.NextCursor == "" {
		t.Fatalf("first page: %+v, %v (snapshot bytes=%d)", page, err, len(raw))
	}
	for _, tx := range pending {
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	count := len(page.Items)
	for f.Cursor = page.NextCursor; f.Cursor != ""; f.Cursor = page.NextCursor {
		page, err = s.Operations(ctx, boss, f)
		if err != nil || page.Partial {
			t.Fatal(page, err)
		}
		for _, r := range page.Items {
			if r.Target == "delayed" {
				t.Fatal("delayed commit entered snapshot")
			}
			count++
		}
	}
	if count != 3 {
		t.Fatal(count)
	}
}

func TestPlatformAuditSnapshotExpiryEvictionAndTokenFormat(t *testing.T) {
	for _, mode := range []string{"expired", "evicted", "old-format"} {
		t.Run(mode, func(t *testing.T) {
			repo, owner := setup(t)
			if _, err := repo.DB.Exec(`INSERT INTO platform.audit(actor,action,target,data,created_at) SELECT 'synthetic','expiry',n::text,'{}',now()-interval '1 minute' FROM generate_series(1,3) n`); err != nil {
				t.Fatal(err)
			}
			q := a.AuditRequest{Scopes: []a.Scope{a.Global}, Until: time.Now().UTC(), Limit: 1}
			var batch a.AuditBatch
			err := repo.View(ctx, func(st a.Store) error { var e error; batch, e = st.ReadAudit(ctx, q); return e })
			if err != nil || len(batch.Snapshot) != 32 || len(batch.Records) != 1 {
				t.Fatal(batch, err)
			}
			q.Snapshot = batch.Snapshot
			switch mode {
			case "expired":
				if _, err = repo.DB.Exec(`UPDATE platform.audit_snapshots SET created_at=now()-interval '25 hours'`); err != nil {
					t.Fatal(err)
				}
			case "evicted":
				if _, err = owner.Exec(`SELECT setval('platform.audit_snapshot_slot',4097,false)`); err != nil {
					t.Fatal(err)
				}
				fresh := q
				fresh.Snapshot = ""
				if err = repo.View(ctx, func(st a.Store) error { _, e := st.ReadAudit(ctx, fresh); return e }); err != nil {
					t.Fatal(err)
				}
				var count int
				if err = repo.DB.QueryRow(`SELECT count(*) FROM platform.audit_snapshots`).Scan(&count); err != nil || count != 1 {
					t.Fatal(count, err)
				}
			case "old-format":
				q.Snapshot = "1|1:2:"
			}
			err = repo.View(ctx, func(st a.Store) error { _, e := st.ReadAudit(ctx, q); return e })
			if !errors.Is(err, a.ErrUnavailable) {
				t.Fatal("invalid snapshot accepted", err)
			}
			if mode == "expired" {
				n, e := repo.Cleanup(ctx, time.Now().UTC())
				if e != nil || n != 1 {
					t.Fatal(n, e)
				}
			}
		})
	}
}

func TestPlatformAuditSingleConnectionPoolDoesNotDeadlock(t *testing.T) {
	s, repo := service(t)
	now := time.Now().UTC()
	for i := range 3 {
		if _, err := repo.DB.Exec(`INSERT INTO platform.audit(actor,action,target,data,created_at) VALUES('synthetic','pool',$1,'{}',$2)`, fmt.Sprint(i), now.Add(-time.Duration(i+1)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	repo.DB.SetMaxOpenConns(1)
	repo.DB.SetMaxIdleConns(1)
	page, err := s.Operations(ctx, boss, a.Filter{Scope: a.Global, Action: "pool", Limit: 1})
	if err != nil || page.Partial || len(page.Items) != 1 || page.NextCursor == "" {
		t.Fatalf("MaxOpenConns=1 deadlocked or failed: page=%+v err=%v", page, err)
	}
	page2, err := s.Operations(ctx, boss, a.Filter{Scope: a.Global, Action: "pool", Limit: 1, Cursor: page.NextCursor})
	if err != nil || page2.Partial || len(page2.Items) != 1 {
		t.Fatalf("second page failed: page=%+v err=%v", page2, err)
	}

	repo.DB.SetMaxOpenConns(2)
	repo.DB.SetMaxIdleConns(2)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			p, e := s.Operations(ctx, boss, a.Filter{Scope: a.Global, Action: "pool", Limit: 2})
			if e != nil || p.Partial || len(p.Items) != 2 {
				t.Errorf("concurrent Operations failed under MaxOpenConns=2: %+v %v", p, e)
			}
		})
	}
	wg.Wait()
}

type strictRangeProvider struct {
	auditProvider
}

func (p *strictRangeProvider) ReadAudit(ctx context.Context, q a.AuditRequest) (a.AuditBatch, error) {
	if !q.From.IsZero() && q.From.After(q.Until) {
		return a.AuditBatch{}, errors.New("inverted time range")
	}
	return p.auditProvider.ReadAudit(ctx, q)
}

func TestPlatformOperationsFutureFromAndDisjointModuleSources(t *testing.T) {
	s, repo := service(t)
	now := time.Now().UTC()
	s.Now = func() time.Time { return now }
	if _, err := repo.DB.Exec(`INSERT INTO platform.audit(actor,action,target,data,created_at,school,module) VALUES('synthetic','grant','obj','{}',$1,'a','resources')`, now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	v := &strictRangeProvider{auditProvider: auditProvider{rows: []a.Record{auditRow("verification", "v1", "a", now.Add(-time.Minute))}}}
	s.AuditProviders = map[string]a.AuditProvider{"verification": v}

	// A-13 (1): From > now should return an empty non-partial page without sending From > Until to providers.
	futurePage, err := s.Operations(ctx, boss, a.Filter{Scope: a.Global, From: now.Add(2 * time.Second), Limit: 10})
	if err != nil || futurePage.Partial || len(futurePage.Items) != 0 || len(v.calls) != 0 {
		t.Fatalf("future From caused provider error or call: page=%+v err=%v calls=%d", futurePage, err, len(v.calls))
	}

	// A-13 (2): Platform admin filtering module=resources with sources=platform,verification must succeed (200) and return matching platform rows.
	disjointFilter := a.Filter{
		Scope:   a.Scope{School: "a", Group: "*", Module: "resources"},
		Sources: []string{"platform", "verification"},
		Limit:   10,
	}
	page, err := s.Operations(ctx, boss, disjointFilter)
	if err != nil || page.Partial || len(page.Items) != 1 || page.Items[0].Source != "platform" {
		t.Fatalf("platform admin disjoint module+sources failed: page=%+v err=%v", page, err)
	}

	// Unauthorized scoped admin explicitly selecting verification must still receive ErrForbidden.
	role(t, s, "res-audit", "platform.audit.read")
	grant(t, s, delegate, "res-audit", "0", a.Scope{School: "a", Group: "*", Module: "resources"})
	_, err = s.Operations(ctx, delegate, disjointFilter)
	deny(t, err)
}

func TestPlatformAuditCursorVariantsAndIndexOrder(t *testing.T) {
	_, repo := service(t)
	owner := repo.DB
	t1 := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	t2 := t1.Add(-time.Second)
	for _, ts := range []time.Time{t1, t1, t2} {
		if _, err := owner.Exec(`INSERT INTO platform.audit(actor,action,target,data,created_at,school,module) VALUES('actor-1','act-1','tgt-1','{}',$1,'buaa','platform')`, ts); err != nil {
			t.Fatal(err)
		}
	}
	var all a.AuditBatch
	if err := repo.View(ctx, func(st a.Store) error {
		var e error
		all, e = st.ReadAudit(ctx, a.AuditRequest{Scopes: []a.Scope{a.Global}, Until: t1.Add(time.Second), Limit: 10})
		return e
	}); err != nil || len(all.Records) != 3 {
		t.Fatalf("initial ReadAudit: %+v %v", all, err)
	}

	// Source > "platform" (e.g. "verification") at t1: platform rows at t1 sort AFTER (t1,"verification",...), so both t1 rows must be included!
	var afterVerif a.AuditBatch
	if err := repo.View(ctx, func(st a.Store) error {
		var e error
		afterVerif, e = st.ReadAudit(ctx, a.AuditRequest{
			Scopes: []a.Scope{a.Global}, Until: t1.Add(time.Second), Snapshot: all.Snapshot, Limit: 10,
			Before: &a.AuditPosition{Time: t1, Source: "verification", ID: "0"},
		})
		return e
	}); err != nil || len(afterVerif.Records) != 3 {
		t.Fatalf("Before verification@t1 expected 3 rows, got %d (%v)", len(afterVerif.Records), err)
	}

	// Source < "platform" (e.g. "billing") at t1: platform rows at t1 sort BEFORE (t1,"billing",...), so only t2 row must remain!
	var afterBilling a.AuditBatch
	if err := repo.View(ctx, func(st a.Store) error {
		var e error
		afterBilling, e = st.ReadAudit(ctx, a.AuditRequest{
			Scopes: []a.Scope{a.Global}, Until: t1.Add(time.Second), Snapshot: all.Snapshot, Limit: 10,
			Before: &a.AuditPosition{Time: t1, Source: "billing", ID: "zzzz"},
		})
		return e
	}); err != nil || len(afterBilling.Records) != 1 || !afterBilling.Records[0].CreatedAt.Equal(t2) {
		t.Fatalf("Before billing@t1 expected 1 row at t2, got %+v (%v)", afterBilling.Records, err)
	}

	// Source == "platform" at t1 with first t1 ID: only second t1 row and t2 row remain.
	var afterFirstPlatform a.AuditBatch
	if err := repo.View(ctx, func(st a.Store) error {
		var e error
		afterFirstPlatform, e = st.ReadAudit(ctx, a.AuditRequest{
			Scopes: []a.Scope{a.Global}, Until: t1.Add(time.Second), Snapshot: all.Snapshot, Limit: 10,
			Before: &a.AuditPosition{Time: t1, Source: "platform", ID: all.Records[0].ID},
		})
		return e
	}); err != nil || len(afterFirstPlatform.Records) != 2 || afterFirstPlatform.Records[0].ID != all.Records[1].ID {
		t.Fatalf("Before platform@t1[0] expected 2 rows starting with %s, got %+v (%v)", all.Records[1].ID, afterFirstPlatform.Records, err)
	}

	// Verify composite merge indexes exist in schema.
	var indexCount int
	if err := repo.DB.QueryRow(`SELECT count(*) FROM pg_indexes WHERE schemaname='platform' AND indexname IN ('platform_audit_merge','platform_audit_scope_merge','platform_audit_actor_merge','platform_audit_target_merge','platform_audit_action_merge')`).Scan(&indexCount); err != nil || indexCount != 5 {
		t.Fatalf("expected 5 aligned merge indexes on platform.audit, got %d (%v)", indexCount, err)
	}
}

func TestPlatformAuditSnapshotActorBucketIsolation(t *testing.T) {
	s, repo := service(t)
	now := time.Now().UTC()
	for i := range 3 {
		if _, err := repo.DB.Exec(`INSERT INTO platform.audit(actor,action,target,data,created_at,school,module) VALUES('synthetic','iso',$1,'{}',$2,'school-a','resources')`, fmt.Sprint(i), now.Add(-time.Duration(i+1)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	role(t, s, "iso-audit", "platform.audit.read")
	noisy := account.Principal{Issuer: "urn:test", Subject: "noisy-operator-bucket-check"}
	grant(t, s, noisy, "iso-audit", "0", school)

	first, err := s.Operations(ctx, boss, a.Filter{Scope: a.Global, Action: "iso", Limit: 1})
	if err != nil || first.Partial || first.NextCursor == "" {
		t.Fatalf("boss page 1 failed: %+v %v", first, err)
	}

	// Another admin floods >4096 first-page snapshot creations.
	for range 4100 {
		if _, err := s.Operations(ctx, noisy, a.Filter{Scope: school, Action: "iso", Limit: 10}); err != nil {
			t.Fatalf("noisy Operations failed: %v", err)
		}
	}

	// boss's active pagination cursor must not have been evicted by noisy's flood.
	second, err := s.Operations(ctx, boss, a.Filter{Scope: a.Global, Action: "iso", Limit: 1, Cursor: first.NextCursor})
	if err != nil || second.Partial || len(second.Items) != 1 {
		t.Fatalf("boss snapshot was evicted by another operator's flood: page=%+v err=%v", second, err)
	}
}
