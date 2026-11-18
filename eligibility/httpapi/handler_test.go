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

func TestEligibilityHTTPActionWhitelistValidationAndProtocolHeaders(t *testing.T) {
	repo := &permissionRepo{}
	authzCalls := 0
	h := httpapi.Handler{
		Service:      e.Service{Repo: repo},
		Authenticate: func(*http.Request) (string, error) { return "admin", nil },
		Authorize: func(_ context.Context, _, _ string) error {
			authzCalls++
			return nil
		},
	}
	mux := http.NewServeMux()
	mux.Handle("/api/v1/platform/eligibility/{school}/{action}", h)

	// A-01: course_enrollment is rejected for non-course-review actions (400), allowed for publish_course (200).
	for _, action := range []string{"content.enter", "resources.upload", "membership.purchase", "publish_mentor"} {
		req := httptest.NewRequest("PUT", "/api/v1/platform/eligibility/buaa/"+action, strings.NewReader(`{"expected_version":"1","conditions":["course_enrollment"]}`))
		req.Header.Set("Idempotency-Key", "bad-"+action)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != 400 {
			t.Fatalf("%s accepted course_enrollment: %d %s", action, w.Code, w.Body.String())
		}
	}
	if repo.writes != 0 {
		t.Fatalf("rejected rule writes reached repository: %d", repo.writes)
	}
	req := httptest.NewRequest("PUT", "/api/v1/platform/eligibility/buaa/publish_course", strings.NewReader(`{"expected_version":"1","conditions":["campus_eligibility","course_enrollment"]}`))
	req.Header.Set("Idempotency-Key", "ok-publish-course")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 || repo.writes != 1 {
		t.Fatalf("publish_course rejected valid course_enrollment: %d %s (writes=%d)", w.Code, w.Body.String(), repo.writes)
	}

	// A-09 & A-15: malformed school/action rejected with 400 BEFORE Authorize is called; whitespace/non-ASCII Idempotency-Key rejected with 400.
	beforeAuthz := authzCalls
	for _, path := range []string{
		"/api/v1/platform/eligibility/" + strings.Repeat("a", 129) + "/content.enter",
		"/api/v1/platform/eligibility/%FF/content.enter",
		"/api/v1/platform/eligibility/%00/content.enter",
		"/api/v1/platform/eligibility/%20/content.enter",
		"/api/v1/platform/eligibility/buaa%20/content.enter",
		"/api/v1/platform/eligibility/buaa/unknown.action",
	} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 400 {
			t.Fatalf("GET %s: expected 400, got %d %s", path, w.Code, w.Body.String())
		}
	}
	if authzCalls != beforeAuthz {
		t.Fatalf("Authorize called on malformed path parameters: before=%d after=%d", beforeAuthz, authzCalls)
	}

	for _, badKey := range []string{"   ", " key", "key ", "key\xff", strings.Repeat("k", 129)} {
		req := httptest.NewRequest("PUT", "/api/v1/platform/eligibility/buaa/content.enter", strings.NewReader(`{"expected_version":"1","conditions":["content_access"]}`))
		req.Header.Set("Idempotency-Key", badKey)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != 400 {
			t.Fatalf("bad Idempotency-Key %q: expected 400, got %d", badKey, w.Code)
		}
	}

	// A-16: nosniff, charset=utf-8, HEAD support, and 405 with Allow + JSON body.
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("HEAD", "/api/v1/platform/eligibility/buaa/content.enter", nil))
	if w.Code != 200 || w.Body.Len() != 0 || w.Header().Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(w.Header().Get("Content-Type"), "charset=utf-8") {
		t.Fatalf("HEAD response mismatch: code=%d len=%d headers=%v", w.Code, w.Body.Len(), w.Header())
	}

	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("PATCH", "/api/v1/platform/eligibility/buaa/content.enter", nil))
	if w.Code != 405 || w.Header().Get("Allow") != "GET, HEAD, PUT, DELETE" || !strings.Contains(w.Body.String(), `"code"`) {
		t.Fatalf("405 response mismatch: code=%d allow=%q body=%q", w.Code, w.Header().Get("Allow"), w.Body.String())
	}
}
