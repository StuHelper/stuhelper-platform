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

type authenticator struct{ err error }

func (a authenticator) Authenticate(*http.Request) (Principal, error) {
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
		{"anonymous", authenticator{ErrUnauthenticated}, authorizer{}, 401},
		{"identity unavailable", authenticator{ErrUnavailable}, authorizer{}, 503},
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
