package postgres_test

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/StuHelper/stuhelper-platform/account"
	a "github.com/StuHelper/stuhelper-platform/admin"
	ah "github.com/StuHelper/stuhelper-platform/admin/httpapi"
	p "github.com/StuHelper/stuhelper-platform/postgres"
	sdk "github.com/StuHelper/stuhelper-platform/sdk/admin"
	"go.uber.org/fx"
)

func TestAuditMissingReasonsClassificationAndCursorPersistence(t *testing.T) {
	s, repo := service(t)
	now := time.Now().UTC()
	s.Now = func() time.Time { return now }

	healthy := &auditProvider{rows: []a.Record{
		auditRow("resources", "2", "a", now.Add(-time.Minute)),
		auditRow("resources", "1", "a", now.Add(-2*time.Minute)),
	}}
	slow := &auditProvider{timeout: true}
	broken := &auditProvider{fail: true}

	s.AuditProviders = map[string]a.AuditProvider{
		"resources":    healthy,
		"catalog":      nil,
		"verification": slow,
		"billing":      broken,
	}
	if _, err := repo.DB.Exec(`INSERT INTO platform.audit(actor,action,target,data,created_at) VALUES('synthetic','obs','obj','{}',$1)`, now.Add(-3*time.Minute)); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	ah.Handler{Service: s, Authenticate: func(*http.Request) (account.Principal, error) { return boss, nil }}.Register(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	client, err := sdk.NewClientWithResponses(server.URL)
	if err != nil {
		t.Fatal(err)
	}

	limit := 1
	resp1, err := client.ListPlatformOperationsWithResponse(ctx, &sdk.ListPlatformOperationsParams{Limit: &limit})
	if err != nil || resp1.JSON200 == nil || !resp1.JSON200.Partial || resp1.JSON200.MissingReasons == nil {
		t.Fatalf("unexpected page 1 response: %+v, err=%v", resp1, err)
	}
	reasons1 := *resp1.JSON200.MissingReasons
	if reasons1["catalog"] != sdk.AdminMissingReasonUnconfigured || reasons1["verification"] != sdk.AdminMissingReasonTimeout || reasons1["billing"] != sdk.AdminMissingReasonUnavailable {
		t.Fatalf("unexpected page 1 missing_reasons: %+v", reasons1)
	}
	if _, hasPlatform := reasons1["platform"]; hasPlatform {
		t.Fatalf("platform unexpectedly missing on page 1: %+v", reasons1)
	}

	// Trigger audit retention cleanup between page 1 and page 2 so platform snapshot expires.
	if _, err = repo.DB.Exec(`INSERT INTO platform.audit(actor,action,target,data,created_at) VALUES('synthetic','expired','obj','{}',$1)`, now.AddDate(-2, 0, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.Cleanup(ctx, now); err != nil {
		t.Fatal(err)
	}

	cursor := resp1.JSON200.NextCursor
	resp2, err := client.ListPlatformOperationsWithResponse(ctx, &sdk.ListPlatformOperationsParams{Limit: &limit, Cursor: &cursor})
	if err != nil || resp2.JSON200 == nil || !resp2.JSON200.Partial || resp2.JSON200.MissingReasons == nil {
		t.Fatalf("unexpected page 2 response: %+v, err=%v", resp2, err)
	}
	reasons2 := *resp2.JSON200.MissingReasons
	if reasons2["platform"] != sdk.AdminMissingReasonSnapshotExpired || reasons2["catalog"] != sdk.AdminMissingReasonUnconfigured || reasons2["verification"] != sdk.AdminMissingReasonTimeout || reasons2["billing"] != sdk.AdminMissingReasonUnavailable {
		t.Fatalf("unexpected page 2 missing_reasons: %+v", reasons2)
	}
}

func TestRetentionStatusBacklogAndFxCleanerLifecycle(t *testing.T) {
	repo, owner := setup(t)
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)

	oldestReq := now.Add(-48 * time.Hour)
	oldestSnap := now.Add(-36 * time.Hour)
	oldestAdmin := now.Add(-30 * time.Hour)
	oldestAudit := now.AddDate(-2, 0, 0)

	liveAt := now.Add(-time.Hour)
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO platform.eligibility_requests VALUES ('a','exp1','d','{}',$1), ('a','live','d','{}',$2)`, []any{oldestReq, liveAt}},
		{`INSERT INTO platform.audit_snapshots VALUES (0,'0123456789abcdef0123456789abcdef',pg_current_snapshot(),1,$1), (1,'fedcba9876543210fedcba9876543210',pg_current_snapshot(),1,$2)`, []any{oldestSnap, liveAt}},
		{`INSERT INTO platform.admin_requests VALUES ('a','exp1','d','{}',$1), ('a','exp2','d','{}',$2), ('a','live','d','{}',$3)`, []any{oldestAdmin, now.Add(-25 * time.Hour), liveAt}},
		{`INSERT INTO platform.audit(actor,action,target,data,created_at) VALUES ('a','act','t','{}',$1), ('a','act','t','{}',$2)`, []any{oldestAudit, liveAt}},
	} {
		if _, err := owner.Exec(stmt.sql, stmt.args...); err != nil {
			t.Fatal(err)
		}
	}

	status, err := repo.RetentionStatus(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if status.EligibilityRequests.ExpiredCount != 1 || status.EligibilityRequests.OldestExpiredAt == nil || !status.EligibilityRequests.OldestExpiredAt.Equal(oldestReq) {
		t.Fatalf("unexpected eligibility_requests status: %+v", status.EligibilityRequests)
	}
	if status.AuditSnapshots.ExpiredCount != 1 || status.AuditSnapshots.OldestExpiredAt == nil || !status.AuditSnapshots.OldestExpiredAt.Equal(oldestSnap) {
		t.Fatalf("unexpected audit_snapshots status: %+v", status.AuditSnapshots)
	}
	if status.AdminRequests.ExpiredCount != 2 || status.AdminRequests.OldestExpiredAt == nil || !status.AdminRequests.OldestExpiredAt.Equal(oldestAdmin) {
		t.Fatalf("unexpected admin_requests status: %+v", status.AdminRequests)
	}
	if status.Audit.ExpiredCount != 1 || status.Audit.OldestExpiredAt == nil || !status.Audit.OldestExpiredAt.Equal(oldestAudit) {
		t.Fatalf("unexpected audit status: %+v", status.Audit)
	}
	if status.TotalExpired() != 5 {
		t.Fatalf("expected TotalExpired=5, got %d", status.TotalExpired())
	}

	// Verify Fx-managed RetentionCleaner drains the backlog and stops cleanly.
	var observed atomic.Int32
	var remainingAfterRun atomic.Int64
	remainingAfterRun.Store(-1)
	cleaner := &p.RetentionCleaner{
		Repo:     repo,
		Interval: 15 * time.Millisecond,
		Now:      func() time.Time { return now },
		Observe: func(rep p.RetentionReport) {
			if rep.Err == nil && observed.Add(1) == 1 {
				remainingAfterRun.Store(rep.Status.TotalExpired())
			}
		},
	}
	app := fx.New(fx.NopLogger, fx.Invoke(func(lc fx.Lifecycle) { cleaner.RegisterLifecycle(lc) }))
	if err = app.Start(ctx); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for observed.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if err = app.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if observed.Load() == 0 || remainingAfterRun.Load() != 0 {
		t.Fatalf("cleaner did not drain expired rows: observed=%d remaining=%d", observed.Load(), remainingAfterRun.Load())
	}
}
