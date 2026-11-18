// Package transport is the common HTTP boundary. Business handlers remain in
// their module and perform resource-specific checks through their services.
package transport

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/felixge/httpsnoop"

	"github.com/StuHelper/stuhelper-platform/account"
	"github.com/StuHelper/stuhelper-platform/internal/api/gen"
	"github.com/StuHelper/stuhelper-platform/module"
)

var (
	ErrUnauthenticated = errors.New("authentication required")
	ErrForbidden       = errors.New("permission denied")
	ErrUnavailable     = errors.New("authorization unavailable")
)

type Principal = account.Principal
type Resource struct{ Type, ID string }
type Authenticator interface {
	Authenticate(*http.Request) (Principal, error)
}
type Authorizer interface {
	Authorize(context.Context, Principal, string, Resource) error
}

type Access string

const (
	Public    Access = "public"
	Protected Access = "protected"
)

type Route struct {
	Method, Path, Module, Capability string
	Access                           Access
	Resource                         func(*http.Request) Resource
	Handler                          http.Handler
}

type unavailableHandler struct {
	http.Handler
	code string
}

// WithUnavailableCode preserves the handler's published 503 error code when
// module readiness prevents the router from calling the handler.
func WithUnavailableCode(handler http.Handler, code string) http.Handler {
	if handler == nil || code == "" {
		return handler
	}
	return unavailableHandler{Handler: handler, code: code}
}

type Error = gen.Problem
type contextKey struct{}
type principalKey struct{}

// Identity is available only after the configured adapter authenticated a request.
func Identity(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalKey{}).(Principal)
	return principal, ok
}

func RequestID(ctx context.Context) string {
	value, _ := ctx.Value(contextKey{}).(string)
	return value
}

// EnsureRequestID reuses the active context request ID or generates a new one,
// ensuring X-Request-ID and X-Content-Type-Options headers are present.
func EnsureRequestID(w http.ResponseWriter, r *http.Request) (*http.Request, string, error) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if id := RequestID(r.Context()); id != "" {
		w.Header().Set("X-Request-ID", id)
		return r, id, nil
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return r, "", err
	}
	id := hex.EncodeToString(raw[:])
	r = r.WithContext(context.WithValue(r.Context(), contextKey{}, id))
	w.Header().Set("X-Request-ID", id)
	return r, id, nil
}

func JSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func problem(w http.ResponseWriter, r *http.Request, status int, code string) {
	JSON(w, status, gen.Problem{Code: code, RequestId: RequestID(r.Context())})
}

// NewHandler rejects ambiguous registrations and requires explicit public or
// protected access. An absent identity adapter never implies anonymous access.
func NewHandler(registry *module.Registry, routes []Route, authn Authenticator, authz Authorizer, logger *slog.Logger) (handler http.Handler, err error) {
	if registry == nil || logger == nil {
		return nil, errors.New("registry and logger are required")
	}
	// ServeMux detects conflicts including patterns that overlap, beyond equality.
	defer func() {
		if recover() != nil {
			handler = nil
			err = errors.New("conflicting or invalid HTTP route")
		}
	}()
	mux := http.NewServeMux()
	for _, route := range routes {
		if route.Handler == nil || !strings.HasPrefix(route.Path, "/api/v1/") || strings.ContainsAny(route.Path, "?#") || !registry.Owns(route.Module, route.Capability) {
			return nil, errors.New("invalid module HTTP route")
		}
		switch route.Method {
		case "GET", "POST", "PUT", "PATCH", "DELETE":
		default:
			return nil, errors.New("invalid HTTP method")
		}
		if route.Access != Public && route.Access != Protected {
			return nil, errors.New("route access must be explicit")
		}
		unavailableCode := "module_unavailable"
		if h, ok := route.Handler.(unavailableHandler); ok {
			unavailableCode = h.code
		}
		mux.Handle(route.Method+" "+route.Path, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			available := registry.CapabilityAvailable(ctx, route.Module, route.Capability)
			cancel()
			if !available {
				problem(w, r, 503, unavailableCode)
				return
			}
			if route.Access == Protected {
				if authn == nil || authz == nil {
					problem(w, r, 503, "identity_unavailable")
					return
				}
				principal, err := authn.Authenticate(r)
				if err != nil || principal.Issuer == "" || principal.Subject == "" {
					if errors.Is(err, ErrUnavailable) {
						problem(w, r, 503, "identity_unavailable")
					} else {
						problem(w, r, 401, "authentication_required")
					}
					return
				}
				resource := Resource{}
				if route.Resource != nil {
					resource = route.Resource(r)
				}
				if err := authz.Authorize(r.Context(), principal, route.Capability, resource); err != nil {
					if errors.Is(err, ErrForbidden) {
						problem(w, r, 403, "permission_denied")
					} else {
						problem(w, r, 503, "authorization_unavailable")
					}
					return
				}
				r = r.WithContext(context.WithValue(r.Context(), principalKey{}, principal))
			}
			route.Handler.ServeHTTP(w, r)
		}))
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { JSON(w, 200, gen.Health{Status: gen.HealthStatusAlive}) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if !registry.ReadyForTraffic(ctx) {
			problem(w, r, 503, "not_ready")
			return
		}
		JSON(w, 200, gen.Health{Status: gen.HealthStatusReady})
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := false
		w = httpsnoop.Wrap(w, httpsnoop.Hooks{
			WriteHeader: func(next httpsnoop.WriteHeaderFunc) httpsnoop.WriteHeaderFunc {
				return func(code int) {
					if code >= 200 || code == http.StatusSwitchingProtocols {
						started = true
					}
					next(code)
				}
			},
			Write: func(next httpsnoop.WriteFunc) httpsnoop.WriteFunc {
				return func(body []byte) (int, error) { started = true; return next(body) }
			},
			Flush: func(next httpsnoop.FlushFunc) httpsnoop.FlushFunc {
				return func() { started = true; next() }
			},
			ReadFrom: func(next httpsnoop.ReadFromFunc) httpsnoop.ReadFromFunc {
				return func(source io.Reader) (int64, error) { started = true; return next(source) }
			},
		})
		var err error
		var id string
		r, id, err = EnsureRequestID(w, r)
		if err != nil {
			http.Error(w, "request unavailable", 503)
			return
		}
		defer func() {
			if recovered := recover(); recovered != nil {
				if recovered == http.ErrAbortHandler {
					panic(http.ErrAbortHandler)
				}
				logger.Error("request handler panic", "request_id", id)
				if started {
					panic(http.ErrAbortHandler)
				}
				problem(w, r, 500, "internal_error")
			}
		}()
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		if strings.HasPrefix(r.URL.Path, "/api/v1/") {
			if _, pattern := mux.Handler(r); pattern == "" {
				var allowed []string
				for _, m := range []string{"GET", "POST", "PUT", "PATCH", "DELETE"} {
					probe := *r
					probe.Method = m
					if _, p := mux.Handler(&probe); p != "" {
						allowed = append(allowed, m)
					}
				}
				if len(allowed) > 0 {
					w.Header().Set("Allow", strings.Join(allowed, ", "))
					problem(w, r, 405, "method_not_allowed")
					return
				}
				problem(w, r, 404, "not_found")
				return
			}
		}
		mux.ServeHTTP(w, r)
	}), nil
}
