package httpapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/StuHelper/stuhelper-platform/account"
	"github.com/StuHelper/stuhelper-platform/admin"
	"github.com/StuHelper/stuhelper-platform/admin/httpapi"
)

type stubIdentity struct{}

func (stubIdentity) PlatformAdmin(_ context.Context, p account.Principal) (bool, error) {
	return p.Subject == "boss", nil
}

type noopStore struct{}

func (noopStore) Role(_ context.Context, id string) (admin.Role, error) {
	if id == "existing" {
		return admin.Role{ID: "existing", Name: "Existing", Capabilities: []string{"resources.manage"}}, nil
	}
	return admin.Role{}, admin.ErrNotFound
}
func (noopStore) Effective(_ context.Context, sub string) (admin.Effective, error) {
	return admin.Effective{AuthorizationMode: admin.ExplicitGrants, Subject: sub, Revision: "0", Grants: []admin.Grant{}}, nil
}
func (noopStore) CreateRole(context.Context, admin.Role) error { return nil }
func (noopStore) Change(context.Context, string, admin.Mutation, bool) (admin.Revision, error) {
	return admin.Revision{Revision: "1"}, nil
}
func (noopStore) Record(context.Context, admin.Record) error                     { return nil }
func (noopStore) Replay(context.Context, string, string, string) ([]byte, error) { return nil, nil }
func (noopStore) SaveReplay(context.Context, string, string, string, []byte, time.Time) error {
	return nil
}
func (noopStore) ReadAudit(context.Context, admin.AuditRequest) (admin.AuditBatch, error) {
	return admin.AuditBatch{Snapshot: "s", Records: []admin.Record{}}, nil
}

type noopRepo struct{}

func (noopRepo) View(_ context.Context, fn func(admin.Store) error) error  { return fn(noopStore{}) }
func (noopRepo) Write(_ context.Context, fn func(admin.Store) error) error { return fn(noopStore{}) }

func TestAdminHTTPHandlerValidationWithoutPostgres(t *testing.T) {
	mux := http.NewServeMux()
	httpapi.Handler{
		Service: admin.Service{Repo: noopRepo{}, Identity: stubIdentity{}},
		Authenticate: func(r *http.Request) (account.Principal, error) {
			sub := r.Header.Get("X-Actor")
			if sub == "" {
				return account.Principal{}, admin.ErrUnauthenticated
			}
			return account.Principal{Issuer: "urn:test", Subject: sub}, nil
		},
	}.Register(mux)

	call := func(method, target, actor, key, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		if actor != "" {
			req.Header.Set("X-Actor", actor)
		}
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}

	if w := call("GET", "/api/v1/platform/admin/role-templates", "", "", ""); w.Code != 401 {
		t.Fatalf("expected 401, got %d", w.Code)
	}
	if w := call("GET", "/api/v1/platform/admin/role-templates", "boss", "", ""); w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if w := call("GET", "/api/v1/platform/admin/roles/missing", "boss", "", ""); w.Code != 404 {
		t.Fatalf("expected 404, got %d", w.Code)
	}
	if w := call("GET", "/api/v1/platform/admin/roles/existing", "boss", "", ""); w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if w := call("POST", "/api/v1/platform/admin/roles", "boss", "", `{"role":{"id":"r","name":"r","capabilities":["a.b"]},"reason":"ok"}`); w.Code != 400 {
		t.Fatalf("expected 400 missing Idempotency-Key, got %d", w.Code)
	}
}

func (noopStore) ListRoles(context.Context, admin.RoleQuery) ([]admin.Role, error) {
	return nil, admin.ErrUnavailable
}

func (noopStore) ListAdministrators(context.Context, admin.AdministratorQuery) ([]admin.AdministratorSummary, error) {
	return nil, admin.ErrUnavailable
}

func (noopStore) ApplyGrantChanges(context.Context, string, string, []admin.GrantTarget, []admin.GrantTarget, string, string, time.Time) (admin.Revision, error) {
	return admin.Revision{}, admin.ErrUnavailable
}
