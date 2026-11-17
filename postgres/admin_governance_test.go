package postgres_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/StuHelper/stuhelper-platform/account"
	a "github.com/StuHelper/stuhelper-platform/admin"
	ah "github.com/StuHelper/stuhelper-platform/admin/httpapi"
	sdk "github.com/StuHelper/stuhelper-platform/sdk/admin"
)

func TestAdminRoleListAndScopedAdministratorLookup(t *testing.T) {
	s, _ := service(t)
	base := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	now := base
	s.Now = func() time.Time { return now }

	createAt := func(offset time.Duration, id, name string, caps ...string) {
		t.Helper()
		now = base.Add(offset)
		if _, err := s.CreateRole(ctx, boss, "create:"+id, a.RoleInput{
			Role:   a.Role{ID: id, Name: name, Capabilities: caps},
			Reason: "create role for list test",
		}); err != nil {
			t.Fatal(err)
		}
	}
	createAt(time.Second, "reader-a", "授权查看甲", "platform.authorization.read")
	createAt(2*time.Second, "mod_v1", "审核角色_100%", "moderation.tasks", "reviews.moderate")
	createAt(2*time.Second, "mod_v2", "审核角色二期", "moderation.tasks", "resources.manage")

	// Unprivileged member cannot list roles or administrators.
	deny(t, func() error { _, err := s.ListRoles(ctx, member, a.RoleFilter{Limit: 10}); return err }())
	deny(t, func() error {
		_, err := s.ListAdministrators(ctx, member, a.AdministratorFilter{Scope: a.Global, Limit: 10})
		return err
	}())

	// Grant scoped authorization.read on school-a to delegate.
	schoolA := a.Scope{School: "buaa", Group: "*", Module: "*"}
	schoolB := a.Scope{School: "other-u", Group: "*", Module: "*"}
	grant(t, s, delegate, "reader-a", "0", schoolA)

	// Scoped reader can list role definitions with pagination and literal wildcard escaping.
	var listedIDs []string
	rf := a.RoleFilter{Limit: 2}
	for {
		page, err := s.ListRoles(ctx, delegate, rf)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range page.Items {
			if r.CreatedAt == nil {
				t.Fatalf("missing created_at on role %s", r.ID)
			}
			listedIDs = append(listedIDs, r.ID)
		}
		if page.NextCursor == "" {
			break
		}
		// Changing filter with an existing cursor must fail with invalid_request.
		if _, err = s.ListRoles(ctx, delegate, a.RoleFilter{Keyword: "mod", Cursor: page.NextCursor, Limit: 2}); !errors.Is(err, a.ErrInvalid) {
			t.Fatalf("want ErrInvalid on tampered role cursor, got %v", err)
		}
		rf.Cursor = page.NextCursor
	}
	if !slices.Equal(listedIDs, []string{"mod_v2", "mod_v1", "reader-a"}) {
		t.Fatalf("unexpected role order: %v", listedIDs)
	}
	literalMatch, err := s.ListRoles(ctx, delegate, a.RoleFilter{Keyword: "_100%", Capability: "moderation.tasks", Limit: 10})
	if err != nil || len(literalMatch.Items) != 1 || literalMatch.Items[0].ID != "mod_v1" {
		t.Fatalf("unexpected filtered roles: %+v, err=%v", literalMatch, err)
	}

	// Set up multi-school administrator grants to verify scope narrowing and isolation.
	userA := account.Principal{Issuer: "urn:test", Subject: "admin-a"}
	userAB := account.Principal{Issuer: "urn:test", Subject: "admin-ab"}
	userB := account.Principal{Issuer: "urn:test", Subject: "admin-b"}
	userGlobal := account.Principal{Issuer: "urn:test", Subject: "admin-global"}

	grant(t, s, userA, "mod_v1", "0", a.Scope{School: "buaa", Group: "g1", Module: "moderation"})
	grant(t, s, userAB, "mod_v1", "0", a.Scope{School: "buaa", Group: "*", Module: "moderation"})
	grant(t, s, userAB, "mod_v2", "1", a.Scope{School: "other-u", Group: "*", Module: "resources"})
	grant(t, s, userB, "mod_v2", "0", schoolB)
	grant(t, s, userGlobal, "mod_v2", "0", a.Scope{School: "*", Group: "*", Module: "resources"})

	// Requesting an unauthorized school returns 403.
	deny(t, func() error {
		_, err := s.ListAdministrators(ctx, delegate, a.AdministratorFilter{Scope: schoolB, Limit: 10})
		return err
	}())

	// Querying with Global scope as scoped delegate narrows results strictly to schoolA ("buaa")
	// and never leaks userB or userAB's other-u grant.
	af := a.AdministratorFilter{Scope: a.Global, Limit: 2}
	seenAdmins := map[string]a.AdministratorSummary{}
	var firstCursor string
	for {
		page, err := s.ListAdministrators(ctx, delegate, af)
		if err != nil {
			t.Fatal(err)
		}
		if firstCursor == "" && page.NextCursor != "" {
			firstCursor = page.NextCursor
		}
		for _, item := range page.Items {
			for _, g := range item.Grants {
				if g.Scope.School != "buaa" {
					t.Fatalf("cross-school grant leaked to buaa delegate: %+v", item)
				}
			}
			seenAdmins[item.Subject] = item
		}
		if page.NextCursor == "" {
			break
		}
		af.Cursor = page.NextCursor
	}
	if _, leaked := seenAdmins[userB.Key()]; leaked {
		t.Fatal("other-u administrator leaked to buaa delegate")
	}
	for _, want := range []account.Principal{delegate, userA, userAB, userGlobal} {
		if _, ok := seenAdmins[want.Key()]; !ok {
			t.Fatalf("missing expected buaa administrator %s", want.Subject)
		}
	}
	if len(seenAdmins[userAB.Key()].Grants) != 1 || seenAdmins[userAB.Key()].Revision != "2" {
		t.Fatalf("unexpected userAB summary: %+v", seenAdmins[userAB.Key()])
	}
	if seenAdmins[userGlobal.Key()].Grants[0].Scope.School != "buaa" {
		t.Fatalf("global grant was not narrowed to buaa: %+v", seenAdmins[userGlobal.Key()])
	}

	// Verify generated SDK client over HTTP and cursor rejection after revocation.
	mux := http.NewServeMux()
	caller := delegate
	ah.Handler{Service: s, Authenticate: func(*http.Request) (account.Principal, error) { return caller, nil }}.Register(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	client, err := sdk.NewClientWithResponses(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	capFilter := "resources.manage"
	adminResp, err := client.ListAdministratorsWithResponse(ctx, &sdk.ListAdministratorsParams{Capability: &capFilter})
	if err != nil || adminResp.JSON200 == nil || len(adminResp.JSON200.Items) != 1 || adminResp.JSON200.Items[0].Subject != userGlobal.Key() {
		t.Fatalf("unexpected SDK administrators response: %+v, err=%v", adminResp, err)
	}
	rolesResp, err := client.ListAdminRolesWithResponse(ctx, &sdk.ListAdminRolesParams{Capability: &capFilter})
	if err != nil || rolesResp.JSON200 == nil || len(rolesResp.JSON200.Items) != 1 || rolesResp.JSON200.Items[0].Id != "mod_v2" {
		t.Fatalf("unexpected SDK roles response: %+v, err=%v", rolesResp, err)
	}

	// Revoke delegate's read grant; old pagination cursor immediately returns 403.
	if _, err = s.Change(ctx, boss, delegate.Key(), "revoke-delegate-read", a.Mutation{
		RoleID:           "reader-a",
		Scope:            schoolA,
		ExpectedRevision: "1",
		Reason:           "revoke read access",
	}, true); err != nil {
		t.Fatal(err)
	}
	if firstCursor == "" {
		t.Fatal("expected pagination cursor")
	}
	deny(t, func() error {
		_, err := s.ListAdministrators(ctx, delegate, a.AdministratorFilter{Scope: a.Global, Cursor: firstCursor, Limit: 2})
		return err
	}())
}

func TestAdminAO03AtomicAndBatchGrantChanges(t *testing.T) {
	s, repo := service(t)
	role(t, s, "delegator", "platform.authorization.manage", "platform.authorization.read", "moderation.tasks", "resources.manage")
	role(t, s, "mod-v1", "moderation.tasks")
	role(t, s, "mod-v2", "moderation.tasks", "resources.manage")
	role(t, s, "sensitive", "billing.refund")

	buaa := a.Scope{School: "buaa", Group: "*", Module: "*"}
	otherSchool := a.Scope{School: "other", Group: "*", Module: "*"}
	grant(t, s, delegate, "delegator", "0", buaa)

	u1 := account.Principal{Issuer: "urn:test", Subject: "reviewer-1"}
	u2 := account.Principal{Issuer: "urn:test", Subject: "reviewer-2"}
	u3 := account.Principal{Issuer: "urn:test", Subject: "reviewer-3"}
	grant(t, s, u1, "mod-v1", "0", buaa)
	grant(t, s, u2, "mod-v1", "0", buaa)

	// 1. Single-subject atomic replacement: mod-v1 -> mod-v2 in one revision step without gap.
	deny(t, func() error {
		_, err := s.ChangeGrants(ctx, delegate, delegate.Key(), "self-atomic", a.GrantChangesInput{
			Add:              []a.GrantTarget{{RoleID: "mod-v1", Scope: buaa}},
			Revoke:           []a.GrantTarget{},
			ExpectedRevision: "1",
			Reason:           "self grant forbidden",
		})
		return err
	}())

	mux := http.NewServeMux()
	ah.Handler{Service: s, Authenticate: func(*http.Request) (account.Principal, error) { return delegate, nil }}.Register(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	client, err := sdk.NewClientWithResponses(server.URL)
	if err != nil {
		t.Fatal(err)
	}

	swapResp, err := client.ChangeAdminGrantsWithResponse(ctx, u1.Key(), &sdk.ChangeAdminGrantsParams{IdempotencyKey: "swap-u1"}, sdk.AdminGrantChangesInput{
		Add:              []sdk.AdminGrantTarget{{RoleId: "mod-v2", Scope: sdk.AdminScope{School: "buaa", Group: "*", Module: "*"}}},
		Revoke:           []sdk.AdminGrantTarget{{RoleId: "mod-v1", Scope: sdk.AdminScope{School: "buaa", Group: "*", Module: "*"}}},
		ExpectedRevision: "1",
		Reason:           "upgrade reviewer role without downtime",
	})
	if err != nil || swapResp.JSON200 == nil || swapResp.JSON200.Revision != "2" {
		t.Fatalf("atomic swap failed: %+v, err=%v", swapResp, err)
	}
	// Replay with identical Idempotency-Key returns revision 2 without duplicating writes.
	replaySwap, err := client.ChangeAdminGrantsWithResponse(ctx, u1.Key(), &sdk.ChangeAdminGrantsParams{IdempotencyKey: "swap-u1"}, sdk.AdminGrantChangesInput{
		Add:              []sdk.AdminGrantTarget{{RoleId: "mod-v2", Scope: sdk.AdminScope{School: "buaa", Group: "*", Module: "*"}}},
		Revoke:           []sdk.AdminGrantTarget{{RoleId: "mod-v1", Scope: sdk.AdminScope{School: "buaa", Group: "*", Module: "*"}}},
		ExpectedRevision: "1",
		Reason:           "upgrade reviewer role without downtime",
	})
	if err != nil || replaySwap.JSON200 == nil || replaySwap.JSON200.Revision != "2" {
		t.Fatalf("atomic swap replay failed: %+v, err=%v", replaySwap, err)
	}
	if err = s.Evaluate(ctx, u1, "resources.manage", buaa); err != nil {
		t.Fatal("new role not active:", err)
	}

	// 2. AO-03 Batch preview (dry_run=true) and per-item execution (dry_run=false).
	u4 := account.Principal{Issuer: "urn:test", Subject: "reviewer-4"}
	batchItems := []sdk.AdminBatchGrantChangeItem{
		{
			// u2: valid swap mod-v1 -> mod-v2 at revision 1
			Subject:          u2.Key(),
			ExpectedRevision: "1",
			Add:              []sdk.AdminGrantTarget{{RoleId: "mod-v2", Scope: sdk.AdminScope{School: "buaa", Group: "*", Module: "*"}}},
			Revoke:           []sdk.AdminGrantTarget{{RoleId: "mod-v1", Scope: sdk.AdminScope{School: "buaa", Group: "*", Module: "*"}}},
		},
		{
			// u1: stale expected_revision "1" (actual is "2") -> conflict
			Subject:          u1.Key(),
			ExpectedRevision: "1",
			Add:              []sdk.AdminGrantTarget{{RoleId: "mod-v1", Scope: sdk.AdminScope{School: "buaa", Group: "*", Module: "*"}}},
			Revoke:           []sdk.AdminGrantTarget{},
		},
		{
			// u3: out-of-scope otherSchool -> forbidden (must not leak target revision)
			Subject:          u3.Key(),
			ExpectedRevision: "0",
			Add:              []sdk.AdminGrantTarget{{RoleId: "mod-v1", Scope: sdk.AdminScope{School: otherSchool.School, Group: "*", Module: "*"}}},
			Revoke:           []sdk.AdminGrantTarget{},
		},
		{
			// u4: missing role -> not_found
			Subject:          u4.Key(),
			ExpectedRevision: "0",
			Add:              []sdk.AdminGrantTarget{{RoleId: "missing-role", Scope: sdk.AdminScope{School: "buaa", Group: "*", Module: "*"}}},
			Revoke:           []sdk.AdminGrantTarget{},
		},
	}

	previewResp, err := client.BatchChangeAdminGrantsWithResponse(ctx, &sdk.BatchChangeAdminGrantsParams{}, sdk.AdminBatchGrantChangesInput{
		DryRun: true,
		Reason: "semester handover preview",
		Items:  batchItems,
	})
	if err != nil || previewResp.JSON200 == nil || !previewResp.JSON200.DryRun || len(previewResp.JSON200.Items) != 4 {
		t.Fatalf("batch preview failed: %+v, err=%v", previewResp, err)
	}
	gotStatuses := []sdk.AdminBatchGrantChangeResultItemStatus{
		previewResp.JSON200.Items[0].Status,
		previewResp.JSON200.Items[1].Status,
		previewResp.JSON200.Items[2].Status,
		previewResp.JSON200.Items[3].Status,
	}
	wantPreview := []sdk.AdminBatchGrantChangeResultItemStatus{
		sdk.AdminBatchGrantChangeResultItemStatusReady,
		sdk.AdminBatchGrantChangeResultItemStatusConflict,
		sdk.AdminBatchGrantChangeResultItemStatusForbidden,
		sdk.AdminBatchGrantChangeResultItemStatusNotFound,
	}
	if !slices.Equal(gotStatuses, wantPreview) {
		t.Fatalf("preview statuses = %v, want %v", gotStatuses, wantPreview)
	}
	if previewResp.JSON200.Items[0].Revision != "2" || previewResp.JSON200.Items[1].Revision != "2" || previewResp.JSON200.Items[2].Revision != "" {
		t.Fatalf("unexpected preview revisions: %+v", previewResp.JSON200.Items)
	}
	// Verify dry_run wrote nothing: u2 is still on mod-v1 (revision 1), lacking resources.manage.
	deny(t, s.Evaluate(ctx, u2, "resources.manage", buaa))

	// Executing dry_run=false without Idempotency-Key is rejected with 400.
	noKeyResp, err := client.BatchChangeAdminGrantsWithResponse(ctx, &sdk.BatchChangeAdminGrantsParams{}, sdk.AdminBatchGrantChangesInput{
		DryRun: false,
		Reason: "semester handover execute",
		Items:  batchItems,
	})
	if err != nil || noKeyResp.StatusCode() != 400 {
		t.Fatalf("expected 400 without Idempotency-Key, got %+v, err=%v", noKeyResp, err)
	}

	batchKey := "batch-handover-2026"
	execResp, err := client.BatchChangeAdminGrantsWithResponse(ctx, &sdk.BatchChangeAdminGrantsParams{IdempotencyKey: &batchKey}, sdk.AdminBatchGrantChangesInput{
		DryRun: false,
		Reason: "semester handover execute",
		Items:  batchItems,
	})
	if err != nil || execResp.JSON200 == nil || execResp.JSON200.DryRun || len(execResp.JSON200.Items) != 4 {
		t.Fatalf("batch execute failed: %+v, err=%v", execResp, err)
	}
	gotExec := []sdk.AdminBatchGrantChangeResultItemStatus{
		execResp.JSON200.Items[0].Status,
		execResp.JSON200.Items[1].Status,
		execResp.JSON200.Items[2].Status,
		execResp.JSON200.Items[3].Status,
	}
	wantExec := []sdk.AdminBatchGrantChangeResultItemStatus{
		sdk.AdminBatchGrantChangeResultItemStatusSucceeded,
		sdk.AdminBatchGrantChangeResultItemStatusConflict,
		sdk.AdminBatchGrantChangeResultItemStatusForbidden,
		sdk.AdminBatchGrantChangeResultItemStatusNotFound,
	}
	if !slices.Equal(gotExec, wantExec) || execResp.JSON200.Items[0].Revision != "2" || execResp.JSON200.Items[1].Revision != "2" {
		t.Fatalf("unexpected batch execute result: %+v", execResp.JSON200.Items)
	}
	if err = s.Evaluate(ctx, u2, "resources.manage", buaa); err != nil {
		t.Fatal("u2 did not receive mod-v2 after batch execution:", err)
	}

	// Replay with identical Idempotency-Key returns identical per-item results without extra audit rows.
	var auditBefore int
	if err = repo.DB.QueryRow(`SELECT count(*) FROM platform.audit WHERE target=$1`, u2.Key()).Scan(&auditBefore); err != nil || auditBefore != 3 {
		t.Fatalf("unexpected u2 audit count: %d, err=%v", auditBefore, err)
	}
	replayBatch, err := client.BatchChangeAdminGrantsWithResponse(ctx, &sdk.BatchChangeAdminGrantsParams{IdempotencyKey: &batchKey}, sdk.AdminBatchGrantChangesInput{
		DryRun: false,
		Reason: "semester handover execute",
		Items:  batchItems,
	})
	if err != nil || replayBatch.JSON200 == nil || replayBatch.JSON200.Items[0].Status != sdk.AdminBatchGrantChangeResultItemStatusSucceeded || replayBatch.JSON200.Items[0].Revision != "2" {
		t.Fatalf("batch replay failed: %+v, err=%v", replayBatch, err)
	}
	var auditAfter int
	if err = repo.DB.QueryRow(`SELECT count(*) FROM platform.audit WHERE target=$1`, u2.Key()).Scan(&auditAfter); err != nil || auditAfter != auditBefore {
		t.Fatalf("replay duplicated audit rows: %d -> %d", auditBefore, auditAfter)
	}

	// Revoking delegate's permission prevents replaying old batch receipts.
	if _, err = s.Change(ctx, boss, delegate.Key(), "revoke-delegator", a.Mutation{
		RoleID:           "delegator",
		Scope:            buaa,
		ExpectedRevision: "1",
		Reason:           "end delegation",
	}, true); err != nil {
		t.Fatal(err)
	}
	revokedReplay, err := client.BatchChangeAdminGrantsWithResponse(ctx, &sdk.BatchChangeAdminGrantsParams{IdempotencyKey: &batchKey}, sdk.AdminBatchGrantChangesInput{
		DryRun: false,
		Reason: "semester handover execute",
		Items:  batchItems,
	})
	if err != nil || revokedReplay.StatusCode() != 403 {
		t.Fatalf("expected 403 after delegator revocation, got %+v, err=%v", revokedReplay, err)
	}
}
