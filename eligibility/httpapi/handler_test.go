package httpapi_test

import (
	"context"
	e "github.com/StuHelper/stuhelper-platform/eligibility"
	"github.com/StuHelper/stuhelper-platform/eligibility/filerepo"
	"github.com/StuHelper/stuhelper-platform/eligibility/httpapi"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuleConfigurationChecksCurrentSchoolPermission(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules.json")
	if err := os.WriteFile(path, []byte(`{"version":"initial","rules":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	allowed := true
	h := httpapi.Handler{Service: e.Service{Repo: filerepo.Repository{Path: path}}, Authenticate: func(r *http.Request) (string, error) {
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
}
