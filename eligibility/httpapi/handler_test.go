package httpapi_test

import (
	"context"
	e "github.com/StuHelper/stuhelper-platform/eligibility"
	"github.com/StuHelper/stuhelper-platform/eligibility/httpapi"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRuleConfigurationChecksCurrentSchoolPermission(t *testing.T) {
	repo := &permissionRepo{}
	allowed := true
	h := httpapi.Handler{Service: e.Service{Repo: repo}, Authenticate: func(r *http.Request) (string, error) {
		if r.Header.Get("Authorization") != "Bearer admin" {
			return "", httpapi.ErrUnauthenticated
		}
		return "admin", nil
	}, Authorize: func(_ context.Context, actor, school string) error {
		if !allowed || actor != "admin" || school != "school" {
			return e.ErrForbidden
		}
		return nil
	}}
	mux := http.NewServeMux()
	mux.Handle("/api/v1/platform/eligibility/{school}/{action}", h)
	call := func(actor, school string, want int) {
		t.Helper()
		r := httptest.NewRequest("PUT", "/api/v1/platform/eligibility/"+school+"/content.enter", strings.NewReader(`{"expected_version":"initial","conditions":["content_access"]}`))
		r.Header.Set("Authorization", actor)
		r.Header.Set("Idempotency-Key", "write")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	call("", "school", 401)
	call("Bearer admin", "other", 403)
	call("Bearer admin", "school", 200)
	allowed = false
	call("Bearer admin", "school", 403)
	if repo.writes != 1 {
		t.Fatal("unauthorized write reached repository", repo.writes)
	}
}

type permissionRepo struct {
	e.Defaults
	writes int
}

func (r *permissionRepo) Apply(_ context.Context, in e.Update) (e.Rule, error) {
	r.writes++
	return e.Rule{Version: "2", Conditions: in.Change.Conditions}, nil
}

func TestPurchaseConfigurationActions(t *testing.T) {
	repo := &permissionRepo{}
	h := httpapi.Handler{Service: e.Service{Repo: repo}, Authenticate: func(*http.Request) (string, error) { return "admin", nil }, Authorize: func(context.Context, string, string) error { return nil }}
	mux := http.NewServeMux()
	mux.Handle("/api/v1/platform/eligibility/{school}/{action}", h)
	for _, action := range []string{"resources.purchase", "membership.purchase", "resources.unlock"} {
		for _, method := range []string{"GET", "PUT"} {
			req := httptest.NewRequest(method, "/api/v1/platform/eligibility/school/"+action, strings.NewReader(`{"expected_version":"initial","conditions":[]}`))
			req.Header.Set("Idempotency-Key", action)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			want := 200
			if action == "resources.unlock" {
				want = 400
			}
			if w.Code != want {
				t.Fatal(action, method, w.Code, w.Body.String())
			}
		}
	}
	if repo.writes != 2 {
		t.Fatal("invalid action reached repository", repo.writes)
	}
}
