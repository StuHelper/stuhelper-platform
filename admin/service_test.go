package admin_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/StuHelper/stuhelper-platform/account"
	"github.com/StuHelper/stuhelper-platform/admin"
)

type memIdentity map[string]bool

func (m memIdentity) PlatformAdmin(_ context.Context, p account.Principal) (bool, error) {
	return m[p.Key()], nil
}

type memStore struct {
	roles     map[string]admin.Role
	revisions map[string]int64
	grants    map[string][]admin.Grant
	replays   map[string]struct {
		hash string
		raw  []byte
	}
	audits []admin.Record
}

func newMemRepo() *memRepo {
	return &memRepo{
		st: &memStore{
			roles:     map[string]admin.Role{},
			revisions: map[string]int64{},
			grants:    map[string][]admin.Grant{},
			replays: map[string]struct {
				hash string
				raw  []byte
			}{},
		},
	}
}

type memRepo struct{ st *memStore }

func (r *memRepo) View(_ context.Context, fn func(admin.Store) error) error  { return fn(r.st) }
func (r *memRepo) Write(_ context.Context, fn func(admin.Store) error) error { return fn(r.st) }

func (s *memStore) Role(_ context.Context, id string) (admin.Role, error) {
	r, ok := s.roles[id]
	if !ok {
		return admin.Role{}, admin.ErrNotFound
	}
	return r, nil
}
func (s *memStore) Effective(_ context.Context, subject string) (admin.Effective, error) {
	return admin.Effective{
		AuthorizationMode: admin.ExplicitGrants,
		Subject:           subject,
		Revision:          strconv.FormatInt(s.revisions[subject], 10),
		Grants:            slices.Clone(s.grants[subject]),
	}, nil
}
func (s *memStore) CreateRole(_ context.Context, r admin.Role) error {
	if _, exists := s.roles[r.ID]; exists {
		return admin.ErrConflict
	}
	s.roles[r.ID] = r
	return nil
}
func (s *memStore) Change(_ context.Context, subject string, m admin.Mutation, revoke bool) (admin.Revision, error) {
	if strconv.FormatInt(s.revisions[subject], 10) != m.ExpectedRevision {
		return admin.Revision{}, admin.ErrConflict
	}
	role, ok := s.roles[m.RoleID]
	if !ok {
		return admin.Revision{}, admin.ErrNotFound
	}
	idx := slices.IndexFunc(s.grants[subject], func(g admin.Grant) bool {
		return g.Role.ID == m.RoleID && g.Scope == m.Scope
	})
	if revoke {
		if idx < 0 {
			return admin.Revision{}, admin.ErrConflict
		}
		s.grants[subject] = slices.Delete(s.grants[subject], idx, idx+1)
	} else {
		if idx >= 0 {
			return admin.Revision{}, admin.ErrConflict
		}
		s.grants[subject] = append(s.grants[subject], admin.Grant{Role: role, Scope: m.Scope})
	}
	s.revisions[subject]++
	return admin.Revision{Revision: strconv.FormatInt(s.revisions[subject], 10)}, nil
}
func (s *memStore) Record(_ context.Context, rec admin.Record) error {
	rec.ID = strconv.Itoa(len(s.audits) + 1)
	s.audits = append(s.audits, rec)
	return nil
}
func (s *memStore) Replay(_ context.Context, actor, key, hash string) ([]byte, error) {
	entry, ok := s.replays[actor+"|"+key]
	if !ok {
		return nil, nil
	}
	if entry.hash != hash {
		return nil, admin.ErrConflict
	}
	return entry.raw, nil
}
func (s *memStore) SaveReplay(_ context.Context, actor, key, hash string, raw []byte, _ time.Time) error {
	s.replays[actor+"|"+key] = struct {
		hash string
		raw  []byte
	}{hash: hash, raw: slices.Clone(raw)}
	return nil
}
func (s *memStore) ReadAudit(_ context.Context, q admin.AuditRequest) (admin.AuditBatch, error) {
	out := admin.AuditBatch{Snapshot: "mem-snap", Records: []admin.Record{}}
	for i := len(s.audits) - 1; i >= 0; i-- {
		rec := s.audits[i]
		if !rec.CreatedAt.Before(q.Until) {
			continue
		}
		out.Records = append(out.Records, rec)
		if len(out.Records) >= q.Limit {
			break
		}
	}
	return out, nil
}

func TestInMemoryAdminServiceAuthorizationDelegationAndReplay(t *testing.T) {
	ctx := context.Background()
	boss := account.Principal{Issuer: "urn:test", Subject: "boss"}
	delegator := account.Principal{Issuer: "urn:test", Subject: "delegator"}
	operator := account.Principal{Issuer: "urn:test", Subject: "operator"}

	now := time.Date(2026, 11, 18, 8, 0, 0, 0, time.UTC)
	repo := newMemRepo()
	svc := admin.Service{
		Repo:     repo,
		Identity: memIdentity{boss.Key(): true},
		Now:      func() time.Time { return now },
	}

	// Scope intersection never derives school from group.
	if _, ok := (admin.Scope{School: "buaa", Group: "g1", Module: "*"}).Intersection(admin.Scope{School: "other", Group: "g1", Module: "*"}); ok {
		t.Fatal("disjoint schools intersected")
	}

	// Non-platform admin cannot create roles.
	if _, err := svc.CreateRole(ctx, delegator, "r1", admin.RoleInput{
		Role:   admin.Role{ID: "res", Name: "资料", Capabilities: []string{"resources.manage"}},
		Reason: "unauthorized",
	}); !errors.Is(err, admin.ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}

	for _, r := range []admin.Role{
		{ID: "delegator", Name: "委托", Capabilities: []string{"platform.authorization.manage", "resources.manage"}},
		{ID: "res", Name: "资料", Capabilities: []string{"resources.manage"}},
		{ID: "plat", Name: "平台", Capabilities: []string{"platform.modules"}},
	} {
		if _, err := svc.CreateRole(ctx, boss, "create-"+r.ID, admin.RoleInput{Role: r, Reason: "init"}); err != nil {
			t.Fatal(err)
		}
	}

	buaaRes := admin.Scope{School: "buaa", Group: "*", Module: "resources"}
	if _, err := svc.Change(ctx, boss, delegator.Key(), "g-del", admin.Mutation{
		RoleID:           "delegator",
		Scope:            buaaRes,
		ExpectedRevision: "0",
		Reason:           "grant delegator",
	}, false); err != nil {
		t.Fatal(err)
	}

	// Delegator cannot grant platform.* capabilities or grant to self.
	if _, err := svc.Change(ctx, delegator, operator.Key(), "g-plat", admin.Mutation{
		RoleID:           "plat",
		Scope:            buaaRes,
		ExpectedRevision: "0",
		Reason:           "escalate",
	}, false); !errors.Is(err, admin.ErrForbidden) {
		t.Fatalf("expected ErrForbidden delegating platform.*, got %v", err)
	}
	if _, err := svc.Change(ctx, delegator, delegator.Key(), "g-self", admin.Mutation{
		RoleID:           "res",
		Scope:            buaaRes,
		ExpectedRevision: "1",
		Reason:           "self",
	}, false); !errors.Is(err, admin.ErrForbidden) {
		t.Fatalf("expected ErrForbidden self-granting, got %v", err)
	}

	// Valid delegation and idempotent replay.
	rev, err := svc.Change(ctx, delegator, operator.Key(), "g-op", admin.Mutation{
		RoleID:           "res",
		Scope:            buaaRes,
		ExpectedRevision: "0",
		Reason:           "assign operator",
	}, false)
	if err != nil || rev.Revision != "1" {
		t.Fatalf("delegation failed: %+v, err=%v", rev, err)
	}
	replay, err := svc.Change(ctx, delegator, operator.Key(), "g-op", admin.Mutation{
		RoleID:           "res",
		Scope:            buaaRes,
		ExpectedRevision: "0",
		Reason:           "assign operator",
	}, false)
	if err != nil || replay.Revision != "1" {
		t.Fatalf("replay failed: %+v, err=%v", replay, err)
	}
	if err = svc.Evaluate(ctx, operator, "resources.manage", buaaRes); err != nil {
		t.Fatal(err)
	}

	// Ensure Templates JSON serializes cleanly.
	if raw, err := json.Marshal(admin.Templates()); err != nil || len(raw) == 0 {
		t.Fatal(err)
	}
}
