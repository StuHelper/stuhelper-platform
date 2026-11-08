package postgres_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/StuHelper/stuhelper-platform/account"
	a "github.com/StuHelper/stuhelper-platform/admin"
	ah "github.com/StuHelper/stuhelper-platform/admin/httpapi"
	sdk "github.com/StuHelper/stuhelper-platform/sdk/admin"
)

type platformIdentityFunc func(context.Context, account.Principal) (bool, error)

func (f platformIdentityFunc) PlatformAdmin(ctx context.Context, p account.Principal) (bool, error) {
	return f(ctx, p)
}

func TestPlatformAdministratorAllCapabilitiesAndDelegation(t *testing.T) {
	s, repo := service(t)
	scopes := []a.Scope{a.Global, school, {School: "other-school", Group: "other-group", Module: "billing"}}
	capabilities := []string{"resources.manage", "billing.refund", "verification.materials.read", "verification.profile.full", "future-module.sensitive"}
	for _, capability := range capabilities {
		for _, scope := range scopes {
			if err := s.Evaluate(ctx, boss, capability, scope); err != nil {
				t.Fatalf("%s %+v: %v", capability, scope, err)
			}
		}
	}
	// No explicit grants are needed for the platform administrator.
	var n int
	if err := repo.DB.QueryRow(`SELECT count(*) FROM platform.admin_grants WHERE subject=$1`, boss.Key()).Scan(&n); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	role(t, s, "sensitive-business", "billing.refund", "verification.materials.read", "resources.manage")
	scope := a.Scope{School: "other-school", Group: "other-group", Module: "billing"}
	mutation := a.Mutation{RoleID: "sensitive-business", Scope: scope, ExpectedRevision: "0", Reason: "explicit delegation"}
	revision, err := s.Change(ctx, boss, member.Key(), "delegate-sensitive", mutation, false)
	if err != nil || revision.Revision != "1" {
		t.Fatal(revision, err)
	}
	for _, capability := range []string{"billing.refund", "verification.materials.read", "resources.manage"} {
		if err = s.Evaluate(ctx, member, capability, scope); err != nil {
			t.Fatal(err)
		}
	}
	deny(t, s.Evaluate(ctx, member, "billing.refund", a.Global))
	deny(t, s.Evaluate(ctx, member, "future-module.sensitive", scope))
	if err = repo.DB.QueryRow(`SELECT count(*) FROM platform.admin_grants WHERE subject=$1`, member.Key()).Scan(&n); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if err = repo.DB.QueryRow(`SELECT count(*) FROM platform.audit WHERE action='role.grant' AND target=$1`, member.Key()).Scan(&n); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	_, err = s.Change(ctx, boss, boss.Key(), "self-all", mutation, false)
	deny(t, err)
	_, err = s.Change(ctx, member, member.Key(), "self-member", mutation, false)
	deny(t, err)
	if err = repo.DB.QueryRow(`SELECT count(*) FROM platform.admin_requests WHERE key IN('self-all','self-member')`).Scan(&n); err != nil || n != 0 {
		t.Fatal("self grants wrote receipts", n, err)
	}
	// The built-in role template name cannot turn its holder into a platform admin.
	role(t, s, "platform", "platform.modules")
	grant(t, s, delegate, "platform", "0", a.Global)
	deny(t, s.Evaluate(ctx, delegate, "billing.refund", scope))
	// Unavailable grant storage does not prevent a trusted administrator's evaluation.
	if err = repo.DB.Close(); err != nil {
		t.Fatal(err)
	}
	if err = s.Evaluate(ctx, boss, "billing.refund", scope); err != nil {
		t.Fatal(err)
	}
	if err = s.Evaluate(ctx, member, "billing.refund", scope); !errors.Is(err, a.ErrUnavailable) {
		t.Fatal(err)
	}
}

func TestPlatformEffectiveModeUsesCurrentTargetIdentity(t *testing.T) {
	s, _ := service(t)
	var platform, unavailable atomic.Bool
	platform.Store(true)
	s.Identity = platformIdentityFunc(func(_ context.Context, p account.Principal) (bool, error) {
		if p == boss && unavailable.Load() {
			return false, a.ErrUnavailable
		}
		return p == boss && platform.Load(), nil
	})
	check := func(who account.Principal, target account.Principal, mode a.AuthorizationMode) {
		t.Helper()
		effective, err := s.Effective(ctx, who, target.Key(), a.Global)
		if err != nil || effective.AuthorizationMode != mode || effective.Subject != target.Key() {
			t.Fatal(effective, err)
		}
	}
	check(boss, boss, a.PlatformAdminAll)
	check(boss, member, a.ExplicitGrants)
	check(member, member, a.ExplicitGrants)
	_, err := s.Effective(ctx, member, boss.Key(), a.Global)
	deny(t, err)
	role(t, s, "query-authorization", "platform.authorization.read")
	grant(t, s, delegate, "query-authorization", "0", a.Global)
	check(delegate, boss, a.PlatformAdminAll)
	check(delegate, delegate, a.ExplicitGrants)
	unavailable.Store(true)
	_, err = s.Effective(ctx, delegate, boss.Key(), a.Global)
	if !errors.Is(err, a.ErrUnavailable) {
		t.Fatal(err)
	}
	unavailable.Store(false)
	platform.Store(false)
	check(boss, boss, a.ExplicitGrants)
	check(delegate, boss, a.ExplicitGrants)
	deny(t, s.Evaluate(ctx, boss, "billing.refund", a.Global))
	platform.Store(true)
	mux := http.NewServeMux()
	ah.Handler{Service: s, Authenticate: func(*http.Request) (account.Principal, error) { return delegate, nil }}.Register(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	client, err := sdk.NewClientWithResponses(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.GetAdminEffectiveWithResponse(ctx, boss.Key(), &sdk.GetAdminEffectiveParams{})
	if err != nil || response.JSON200 == nil || string(response.JSON200.AuthorizationMode) != "platform_admin_all" {
		t.Fatal(response, err)
	}
	response, err = client.GetAdminEffectiveWithResponse(ctx, member.Key(), &sdk.GetAdminEffectiveParams{})
	if err != nil || response.JSON200 == nil || string(response.JSON200.AuthorizationMode) != "explicit_grants" {
		t.Fatal(response, err)
	}
}
