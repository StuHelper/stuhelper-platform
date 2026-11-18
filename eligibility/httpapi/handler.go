package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/StuHelper/stuhelper-platform/account"
	e "github.com/StuHelper/stuhelper-platform/eligibility"
	"github.com/StuHelper/stuhelper-platform/transport"
)

var (
	ErrUnauthenticated = errors.New("unauthenticated")
	ErrInvalidRequest  = errors.New("invalid_request")
)

const unavailableCode = "unavailable"

type Handler struct {
	Service      e.Service
	Authenticate func(*http.Request) (string, error)
	Authorize    func(context.Context, string, string) error
}

// NewRoutes constructs transport.Route entries backed by account.Principal
// authentication and per-school authorization.
func NewRoutes(service e.Service, authn transport.Authenticator, authz func(context.Context, account.Principal, string) error) []transport.Route {
	return Handler{
		Service: service,
		Authenticate: func(r *http.Request) (string, error) {
			if authn == nil {
				return "", e.ErrUnavailable
			}
			p, err := authn.Authenticate(r)
			if errors.Is(err, transport.ErrUnauthenticated) {
				return "", ErrUnauthenticated
			}
			if errors.Is(err, transport.ErrForbidden) {
				return "", e.ErrForbidden
			}
			if err != nil {
				return "", e.ErrUnavailable
			}
			if !p.Valid() {
				return "", ErrUnauthenticated
			}
			return p.Key(), nil
		},
		Authorize: func(ctx context.Context, actor, school string) error {
			if authz == nil {
				return e.ErrUnavailable
			}
			p, err := account.Parse(actor)
			if err != nil {
				return ErrUnauthenticated
			}
			return authz(ctx, p, school)
		},
	}.Routes()
}

// Routes exports GET and PUT eligibility configuration routes for application.New.
func (h Handler) Routes() []transport.Route {
	handler := transport.WithUnavailableCode(h, unavailableCode)
	return []transport.Route{
		{Method: "GET", Path: "/api/v1/platform/eligibility/{school}/{action}", Module: "platform", Capability: "platform.modules", Access: transport.Public, Handler: handler},
		{Method: "PUT", Path: "/api/v1/platform/eligibility/{school}/{action}", Module: "platform", Capability: "platform.modules", Access: transport.Public, Handler: handler},
	}
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	var reqID string
	var idErr error
	r, reqID, idErr = transport.EnsureRequestID(w, r)
	fail := func(err error) {
		code, status := unavailableCode, 503
		switch {
		case errors.Is(err, ErrUnauthenticated):
			code, status = "unauthenticated", 401
		case errors.Is(err, e.ErrForbidden):
			code, status = "forbidden", 403
		case errors.Is(err, ErrInvalidRequest):
			code, status = "invalid_request", 400
		case errors.Is(err, e.ErrRule):
			code, status = "invalid_rule", 400
		case errors.Is(err, e.ErrConflict):
			code, status = "conflict", 409
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "request_id": reqID})
	}
	if idErr != nil {
		fail(e.ErrUnavailable)
		return
	}
	if h.Authenticate == nil || h.Authorize == nil {
		fail(e.ErrUnavailable)
		return
	}
	actor, err := h.Authenticate(r)
	if err != nil {
		fail(err)
		return
	}
	school, action := r.PathValue("school"), r.PathValue("action")
	if err = h.Authorize(r.Context(), actor, school); err != nil {
		fail(err)
		return
	}
	var result e.Rule
	switch r.Method {
	case "GET":
		result, err = h.Service.Get(r.Context(), school, action)
	case "PUT":
		var in e.Change
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		d.DisallowUnknownFields()
		if d.Decode(&in) != nil || d.Decode(new(any)) != io.EOF || len(r.Header.Values("Idempotency-Key")) != 1 {
			fail(ErrInvalidRequest)
			return
		}
		result, err = h.Service.Set(r.Context(), actor, r.Header.Get("Idempotency-Key"), school, action, in)
	default:
		w.Header().Set("Allow", "GET, PUT")
		w.WriteHeader(405)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "method_not_allowed", "request_id": reqID})
		return
	}
	if err != nil {
		fail(err)
		return
	}
	_ = json.NewEncoder(w).Encode(result)
}
