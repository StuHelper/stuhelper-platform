package transport

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/StuHelper/stuhelper-platform/module"
	"go.uber.org/fx"
)

type authenticator struct {
	principal *Principal
	err       error
}

func (a authenticator) Authenticate(*http.Request) (Principal, error) {
	if a.principal != nil {
		return *a.principal, a.err
	}
	return Principal{Issuer: "test-issuer", Subject: "synthetic-user"}, a.err
}

func TestPanicAfterResponseStartsAbortsHTTPStream(t *testing.T) {
	for _, http2 := range []bool{false, true} {
		for _, mode := range []string{"header", "body", "flush", "copy"} {
			t.Run(fmt.Sprintf("http2=%v/%s", http2, mode), func(t *testing.T) {
				var logs strings.Builder
				route := Route{Method: "GET", Path: "/api/v1/example", Module: "example", Capability: "example.read", Access: Public, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch mode {
					case "header":
						w.WriteHeader(200)
					case "body":
						_, _ = w.Write([]byte("partial"))
					case "flush":
						w.(http.Flusher).Flush()
					case "copy":
						_, _ = io.Copy(w, struct{ io.Reader }{strings.NewReader("partial")})
					}
					panic("private fixture detail")
				})}
				h, err := NewHandler(registry(t, true), []Route{route}, nil, nil, slog.New(slog.NewJSONHandler(&logs, nil)))
				if err != nil {
					t.Fatal(err)
				}
				server := httptest.NewUnstartedServer(h)
				server.EnableHTTP2 = http2
				server.StartTLS()
				defer server.Close()
				client := server.Client()
				defer client.CloseIdleConnections()
				response, requestErr := client.Get(server.URL + "/api/v1/example")
				var body []byte
				var readErr error
				if requestErr == nil {
					body, readErr = io.ReadAll(response.Body)
					response.Body.Close()
				}
				if requestErr == nil && readErr == nil {
					t.Fatalf("panic returned a complete response: %s", body)
				}
				if strings.Contains(string(body)+logs.String(), "private fixture detail") || strings.Contains(string(body), "internal_error") {
					t.Fatal("panic leaked or appended error payload")
				}
			})
		}
	}
}

type authorizer struct{ err error }

func (a authorizer) Authorize(context.Context, Principal, string, Resource) error { return a.err }

func registry(t *testing.T, enabled bool) *module.Registry {
	t.Helper()
	var selected []string
	if enabled {
		selected = []string{"example"}
	}
	r, err := module.New([]module.Definition{{Descriptor: module.Descriptor{ID: "example", Version: "development", API: 1, Capabilities: []string{"example.read"}}}}, selected)
	if err != nil {
		t.Fatal(err)
	}
	app := fx.New(fx.NopLogger, fx.Invoke(func(lc fx.Lifecycle) { r.RegisterLifecycle(lc) }))
	if err = app.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	return r
}

func TestProtectedRouteNeverBypassesMissingOrDeniedAuthorization(t *testing.T) {
	for _, tt := range []struct {
		name   string
		authn  Authenticator
		authz  Authorizer
		status int
	}{
		{"missing", nil, nil, 503},
		{"anonymous", authenticator{err: ErrUnauthenticated}, authorizer{}, 401},
		{"csrf or revoked authn forbidden", authenticator{err: ErrForbidden}, authorizer{}, 403},
		{"invalid control char principal", authenticator{principal: &Principal{Issuer: "bad\x00issuer", Subject: "user"}}, authorizer{}, 401},
		{"invalid utf8 principal", authenticator{principal: &Principal{Issuer: "issuer", Subject: "bad\xffuser"}}, authorizer{}, 401},
		{"identity unavailable", authenticator{err: ErrUnavailable}, authorizer{}, 503},
		{"denied", authenticator{}, authorizer{ErrForbidden}, 403},
		{"policy unavailable", authenticator{}, authorizer{ErrUnavailable}, 503},
		{"authorized", authenticator{}, authorizer{}, 200},
	} {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			route := Route{Method: "GET", Path: "/api/v1/example", Module: "example", Capability: "example.read", Access: Protected, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				if _, ok := Identity(r.Context()); !ok {
					t.Error("principal not propagated")
				}
				JSON(w, 200, map[string]bool{"ok": true})
			})}
			h, err := NewHandler(registry(t, true), []Route{route}, tt.authn, tt.authz, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			req := httptest.NewRequest("GET", "/api/v1/example", nil)
			req.Header.Set("X-Request-ID", "untrusted")
			h.ServeHTTP(w, req)
			if w.Code != tt.status || called != (tt.status == 200) {
				t.Fatalf("status %d body %s handler-called %v", w.Code, w.Body, called)
			}
			if id := w.Header().Get("X-Request-ID"); len(id) != 32 || id == "untrusted" {
				t.Fatal("request ID not generated")
			}
		})
	}
}

func TestDisabledModuleAndInvalidRoutes(t *testing.T) {
	route := Route{Method: "GET", Path: "/api/v1/example", Module: "example", Capability: "example.read", Access: Public, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("disabled handler executed") })}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	r := registry(t, false)
	h, err := NewHandler(r, []Route{route}, nil, nil, log)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/example", nil))
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
	if _, err = NewHandler(r, []Route{route, route}, nil, nil, log); err == nil {
		t.Fatal("route conflict accepted")
	}
	route.Access = ""
	if _, err = NewHandler(r, []Route{route}, nil, nil, log); err == nil {
		t.Fatal("implicit access accepted")
	}
	route.Access = Public
	route.Capability = "other.admin"
	if _, err = NewHandler(r, []Route{route}, nil, nil, log); err == nil {
		t.Fatal("undeclared capability accepted")
	}
}

func TestPanicDoesNotExposeDetails(t *testing.T) {
	var logs strings.Builder
	route := Route{Method: "GET", Path: "/api/v1/example", Module: "example", Capability: "example.read", Access: Public, Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("private fixture detail") })}
	h, err := NewHandler(registry(t, true), []Route{route}, nil, nil, slog.New(slog.NewJSONHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/example", nil))
	if w.Code != 500 || strings.Contains(w.Body.String()+logs.String(), "private fixture detail") {
		t.Fatal("panic detail exposed")
	}
}

func TestPublicRouteOptionalSourceLimiter(t *testing.T) {
	limiter := &SourceLimiter{Limit: 2}
	route := Route{
		Method:     "GET",
		Path:       "/api/v1/example",
		Module:     "example",
		Capability: "example.read",
		Access:     Public,
		Limiter:    limiter,
		Handler:    http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { JSON(w, 200, map[string]bool{"ok": true}) }),
	}
	h, err := NewHandler(registry(t, true), []Route{route}, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/v1/example", nil)
		req.RemoteAddr = "198.51.100.10:12345"
		h.ServeHTTP(w, req)
		want := 200
		if i == 2 {
			want = 429
		}
		if w.Code != want {
			t.Fatalf("attempt %d: got %d body %s", i, w.Code, w.Body.String())
		}
	}
}

func TestUnknownAPIRouteAndMethodMismatchReturnJSONProblem(t *testing.T) {
	route := Route{Method: "GET", Path: "/api/v1/example", Module: "example", Capability: "example.read", Access: Public, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		JSON(w, 200, map[string]bool{"ok": true})
	})}
	h, err := NewHandler(registry(t, true), []Route{route}, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}

	// Unknown /api/v1/* path returns JSON 404 not_found with matching X-Request-ID.
	w404 := httptest.NewRecorder()
	h.ServeHTTP(w404, httptest.NewRequest("GET", "/api/v1/unknown-endpoint", nil))
	if w404.Code != 404 || !strings.HasPrefix(w404.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("expected JSON 404, got %d (%s): %s", w404.Code, w404.Header().Get("Content-Type"), w404.Body.String())
	}
	reqID404 := w404.Header().Get("X-Request-ID")
	if len(reqID404) != 32 || !strings.Contains(w404.Body.String(), `"code":"not_found"`) || !strings.Contains(w404.Body.String(), `"request_id":"`+reqID404+`"`) {
		t.Fatalf("unexpected 404 body: %s (X-Request-ID=%s)", w404.Body.String(), reqID404)
	}

	// Unmatched method on known /api/v1/* path returns JSON 405 method_not_allowed with Allow header.
	w405 := httptest.NewRecorder()
	h.ServeHTTP(w405, httptest.NewRequest("POST", "/api/v1/example", strings.NewReader(`{}`)))
	if w405.Code != 405 || w405.Header().Get("Allow") != "GET" {
		t.Fatalf("expected 405 Allow=GET, got %d Allow=%q: %s", w405.Code, w405.Header().Get("Allow"), w405.Body.String())
	}
	reqID405 := w405.Header().Get("X-Request-ID")
	if len(reqID405) != 32 || !strings.Contains(w405.Body.String(), `"code":"method_not_allowed"`) || !strings.Contains(w405.Body.String(), `"request_id":"`+reqID405+`"`) {
		t.Fatalf("unexpected 405 body: %s (X-Request-ID=%s)", w405.Body.String(), reqID405)
	}
}

func TestExplicitHeadRouteUsesItsOwnProtectedHandler(t *testing.T) {
	meta := Route{Method: http.MethodHead, Path: "/api/v1/example/content", Module: "example", Capability: "example.read", Access: Protected}
	get := meta
	get.Method = http.MethodGet
	get.Handler = http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("HEAD ran the content handler") })
	for _, tt := range []struct {
		name  string
		authn Authenticator
		authz Authorizer
		want  int
	}{
		{"anonymous", authenticator{err: ErrUnauthenticated}, authorizer{}, 401},
		{"revoked", authenticator{}, authorizer{ErrForbidden}, 403},
		{"authorized", authenticator{}, authorizer{}, 200},
	} {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			meta.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				if _, ok := Identity(r.Context()); !ok {
					t.Fatal("missing authenticated identity")
				}
				w.Header().Set("ETag", "fixture-revision")
			})
			h, err := NewHandler(registry(t, true), []Route{get, meta}, tt.authn, tt.authz, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(http.MethodHead, meta.Path, nil))
			if w.Code != tt.want || called != (tt.want == 200) {
				t.Fatal("unexpected authorization or handler", w.Code, called)
			}
			if called && (w.Header().Get("ETag") != "fixture-revision" || w.Body.Len() != 0) {
				t.Fatal("invalid metadata response")
			}
		})
	}
}
