package admin_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/StuHelper/stuhelper-platform/account"
	a "github.com/StuHelper/stuhelper-platform/admin"
)

type spyRepo struct {
	inView         atomic.Bool
	viewCount      int
	effectiveCalls int
	roles          map[string]a.Role
	effective      map[string]a.Effective
}

func (r *spyRepo) View(_ context.Context, fn func(a.Store) error) error {
	r.viewCount++
	r.inView.Store(true)
	defer r.inView.Store(false)
	return fn(&spyStore{repo: r})
}

func (r *spyRepo) Write(_ context.Context, fn func(a.Store) error) error {
	return fn(&spyStore{repo: r})
}

type spyStore struct {
	repo *spyRepo
}

func (s *spyStore) Role(_ context.Context, id string) (a.Role, error) {
	role, ok := s.repo.roles[id]
	if !ok {
		return a.Role{}, a.ErrNotFound
	}
	return role, nil
}

func (s *spyStore) Effective(_ context.Context, subject string) (a.Effective, error) {
	s.repo.effectiveCalls++
	if eff, ok := s.repo.effective[subject]; ok {
		return eff, nil
	}
	return a.Effective{Subject: subject, Revision: "0", Grants: []a.Grant{}, AuthorizationMode: a.ExplicitGrants}, nil
}

func (s *spyStore) CreateRole(_ context.Context, r a.Role) error {
	s.repo.roles[r.ID] = r
	return nil
}

func (s *spyStore) Change(_ context.Context, _ string, _ a.Mutation, _ bool) (a.Revision, error) {
	return a.Revision{Revision: "1"}, nil
}

func (s *spyStore) Record(context.Context, a.Record) error { return nil }
func (s *spyStore) Replay(context.Context, string, string, string, time.Time) ([]byte, error) {
	return nil, nil
}
func (s *spyStore) SaveReplay(context.Context, string, string, string, []byte, time.Time) error {
	return nil
}
func (s *spyStore) ReadAudit(context.Context, a.AuditRequest) (a.AuditBatch, error) {
	return a.AuditBatch{}, nil
}

type identityFunc func(context.Context, account.Principal) (bool, error)

func (f identityFunc) PlatformAdmin(ctx context.Context, p account.Principal) (bool, error) {
	return f(ctx, p)
}

func TestEffectiveDecouplesTargetIdentityFromViewAndAllowsRevokedTargetInspection(t *testing.T) {
	ctx := context.Background()
	boss := account.Principal{Issuer: "urn:test", Subject: "boss"}
	departed := account.Principal{Issuer: "urn:test", Subject: "departed"}
	repo := &spyRepo{
		roles: map[string]a.Role{},
		effective: map[string]a.Effective{
			departed.Key(): {
				Subject:           departed.Key(),
				Revision:          "3",
				AuthorizationMode: a.ExplicitGrants,
				Grants: []a.Grant{{
					Role:  a.Role{ID: "resources", Name: "资料", Capabilities: []string{"resources.manage"}},
					Scope: a.Scope{School: "buaa", Group: "*", Module: "resources"},
				}},
			},
		},
	}

	calledTargetInsideTx := false
	targetErr := a.ErrForbidden
	svc := a.Service{
		Repo: repo,
		Identity: identityFunc(func(_ context.Context, p account.Principal) (bool, error) {
			if p == boss {
				return true, nil
			}
			if repo.inView.Load() {
				calledTargetInsideTx = true
			}
			return false, targetErr
		}),
	}

	for _, te := range []error{a.ErrForbidden, a.ErrUnauthenticated} {
		targetErr = te
		calledTargetInsideTx = false
		eff, err := svc.Effective(ctx, boss, departed.Key(), a.Global)
		if err != nil {
			t.Fatalf("target err %v leaked as caller error: %v", te, err)
		}
		if calledTargetInsideTx {
			t.Fatalf("target identity queried while DB View transaction was open")
		}
		if eff.AuthorizationMode != a.ExplicitGrants || eff.Revision != "3" || len(eff.Grants) != 1 {
			t.Fatalf("unexpected effective response for revoked target: %+v", eff)
		}
	}
}

func TestChangeLoadsDelegatorEffectiveGrantsOnceAndRejectsMalformedInputs(t *testing.T) {
	ctx := context.Background()
	boss := account.Principal{Issuer: "urn:test", Subject: "boss"}
	delegate := account.Principal{Issuer: "urn:test", Subject: "delegate"}
	member := account.Principal{Issuer: "urn:test", Subject: "member"}

	caps := []string{
		"resources.read", "resources.write", "resources.manage", "resources.coverage.read",
		"catalog.read", "catalog.maintain", "reviews.read", "reviews.moderate",
		"moderation.review", "moderation.manage",
	}
	delegatorCaps := append([]string{"platform.authorization.manage"}, caps...)
	scope := a.Scope{School: "buaa", Group: "*", Module: "*"}

	repo := &spyRepo{
		roles: map[string]a.Role{
			"bundle": {ID: "bundle", Name: "Bundle", Capabilities: caps},
		},
		effective: map[string]a.Effective{
			delegate.Key(): {
				Subject:  delegate.Key(),
				Revision: "1",
				Grants: []a.Grant{{
					Role:  a.Role{ID: "delegator", Name: "Delegator", Capabilities: delegatorCaps},
					Scope: scope,
				}},
			},
		},
	}
	svc := a.Service{
		Repo: repo,
		Identity: identityFunc(func(_ context.Context, p account.Principal) (bool, error) {
			return p == boss, nil
		}),
	}

	if _, err := svc.Change(ctx, delegate, member.Key(), "key-1", a.Mutation{
		RoleID: "bundle", Scope: scope, ExpectedRevision: "0", Reason: "delegate bundle",
	}, false); err != nil {
		t.Fatalf("Change failed: %v", err)
	}
	if repo.effectiveCalls != 1 {
		t.Fatalf("expected 1 Effective lookup for delegator, got %d (N+1)", repo.effectiveCalls)
	}

	// A-15 & A-16: reject whitespace-only reason/name, trailing-space scope, whitespace key, and malformed capabilities.
	for _, badCap := range []string{"platform", "platform.", ".read", "a..b", "a.-b"} {
		if _, err := svc.CreateRole(ctx, boss, "role-bad", a.RoleInput{
			Role:   a.Role{ID: "bad", Name: "Bad", Capabilities: []string{badCap}},
			Reason: "test",
		}); !errors.Is(err, a.ErrInvalid) {
			t.Fatalf("capability %q should fail with ErrInvalid, got %v", badCap, err)
		}
	}
	if _, err := svc.CreateRole(ctx, boss, "role-blank-name", a.RoleInput{
		Role:   a.Role{ID: "valid", Name: "   ", Capabilities: []string{"resources.manage"}},
		Reason: "test",
	}); !errors.Is(err, a.ErrInvalid) {
		t.Fatalf("blank role Name accepted: %v", err)
	}
	if _, err := svc.Change(ctx, boss, member.Key(), "key-blank-reason", a.Mutation{
		RoleID: "bundle", Scope: scope, ExpectedRevision: "0", Reason: "   ",
	}, false); !errors.Is(err, a.ErrInvalid) {
		t.Fatalf("blank Reason accepted: %v", err)
	}
	if _, err := svc.Change(ctx, boss, member.Key(), "   ", a.Mutation{
		RoleID: "bundle", Scope: scope, ExpectedRevision: "0", Reason: "valid",
	}, false); !errors.Is(err, a.ErrInvalid) {
		t.Fatalf("blank Idempotency-Key accepted: %v", err)
	}
	if _, err := svc.Change(ctx, boss, member.Key(), "key-space-scope", a.Mutation{
		RoleID: "bundle", Scope: a.Scope{School: "buaa ", Group: "*", Module: "*"}, ExpectedRevision: "0", Reason: "valid",
	}, false); !errors.Is(err, a.ErrInvalid) {
		t.Fatalf("trailing-space Scope accepted: %v", err)
	}

	// A-17: Templates returns stable i18n label_key values, includes platform.eligibility.manage, and does not open a DB View.
	viewsBefore := repo.viewCount
	templates, err := svc.Templates(ctx, delegate)
	if err != nil || len(templates) != 6 || repo.viewCount != viewsBefore {
		t.Fatalf("Templates unexpected result: len=%d err=%v views=%d->%d", len(templates), err, viewsBefore, repo.viewCount)
	}
	foundEligibility := false
	for _, tpl := range templates {
		if tpl.LabelKey != "platform.admin.role_template."+tpl.ID {
			t.Fatalf("template %s missing expected label_key, got %q", tpl.ID, tpl.LabelKey)
		}
		if tpl.ID == "platform" {
			for _, c := range tpl.Capabilities {
				if c == "platform.eligibility.manage" {
					foundEligibility = true
				}
			}
		}
	}
	if !foundEligibility {
		t.Fatalf("platform template missing platform.eligibility.manage: %+v", templates[0])
	}
}
