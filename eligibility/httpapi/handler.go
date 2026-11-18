package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/StuHelper/stuhelper-platform/account"
	e "github.com/StuHelper/stuhelper-platform/eligibility"
	"github.com/StuHelper/stuhelper-platform/transport"
)

var ErrUnauthenticated = errors.New("unauthenticated")

var ErrInvalidRequest = errors.New("invalid request")

const unavailableCode = "unavailable"

type Handler struct {
	Service      e.Service
	Authenticate func(*http.Request) (string, error)
	Authorize    func(context.Context, string, string) error
	// Admission or Admissions supplies runtime fact evaluation for GET /api/v1/platform/eligibility-check.
	Admission  *e.Admission
	Admissions map[string]e.Admission
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

// Routes exports the configuration and preflight endpoints for application.New.
func (h Handler) Routes() []transport.Route {
	handler := transport.WithUnavailableCode(h, unavailableCode)
	return []transport.Route{
		{Method: "GET", Path: "/api/v1/platform/eligibility-check", Module: "platform", Capability: "platform.modules", Access: transport.Public, Handler: handler},
		{Method: "GET", Path: "/api/v1/platform/eligibility/{school}", Module: "platform", Capability: "platform.modules", Access: transport.Public, Handler: handler},
		{Method: "DELETE", Path: "/api/v1/platform/eligibility/{school}/{action}", Module: "platform", Capability: "platform.modules", Access: transport.Public, Handler: handler},
		{Method: "GET", Path: "/api/v1/platform/eligibility/{school}/{action}", Module: "platform", Capability: "platform.modules", Access: transport.Public, Handler: handler},
		{Method: "PUT", Path: "/api/v1/platform/eligibility/{school}/{action}", Module: "platform", Capability: "platform.modules", Access: transport.Public, Handler: handler},
	}
}

func (h Handler) Register(mux *http.ServeMux) {
	mux.Handle("GET /api/v1/platform/eligibility/{school}", h)
	mux.Handle("/api/v1/platform/eligibility/{school}/{action}", h)
	mux.Handle("GET /api/v1/platform/eligibility-check", h)
}

func (h Handler) resolveAdmission(school string) (e.Admission, bool) {
	if h.Admissions != nil {
		adm, ok := h.Admissions[school]
		return adm, ok
	}
	if h.Admission != nil {
		return *h.Admission, true
	}
	return e.Admission{}, false
}

func writeFailure(w http.ResponseWriter, r *http.Request, err error) {
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
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "request_id": transport.RequestID(r.Context())})
}

func decodeIdempotentJSON(w http.ResponseWriter, r *http.Request, out any) error {
	if len(r.Header.Values("Idempotency-Key")) != 1 {
		return ErrInvalidRequest
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil || d.Decode(new(any)) != io.EOF {
		return ErrInvalidRequest
	}
	return nil
}

func (h Handler) serveCheck(w http.ResponseWriter, r *http.Request, actor string) {
	if r.Method != "GET" {
		w.Header().Set("Allow", "GET")
		w.WriteHeader(405)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "method_not_allowed", "request_id": transport.RequestID(r.Context())})
		return
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeFailure(w, r, e.ErrRule)
		return
	}
	for k, v := range q {
		if (k != "school" && k != "actions") || len(v) != 1 || v[0] == "" {
			writeFailure(w, r, e.ErrRule)
			return
		}
	}
	school := q.Get("school")
	rawActions := q.Get("actions")
	if !e.ValidSchool(school) || rawActions == "" {
		writeFailure(w, r, e.ErrRule)
		return
	}
	actions := strings.Split(rawActions, ",")
	adm, ok := h.resolveAdmission(school)
	if !ok {
		// Distinguish cross-school requests against a single configured school from unconfigured preflight.
		if h.Admission != nil && h.Admission.School != "" && school != h.Admission.School {
			writeFailure(w, r, e.ErrForbidden)
			return
		}
		if len(h.Admissions) > 0 {
			writeFailure(w, r, e.ErrForbidden)
			return
		}
		writeFailure(w, r, e.ErrUnavailable)
		return
	}
	batch, err := adm.Preview(r.Context(), actor, school, actions)
	if err != nil {
		writeFailure(w, r, err)
		return
	}
	_ = json.NewEncoder(w).Encode(batch)
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	var idErr error
	r, _, idErr = transport.EnsureRequestID(w, r)
	if idErr != nil {
		writeFailure(w, r, e.ErrUnavailable)
		return
	}
	if h.Authenticate == nil {
		writeFailure(w, r, e.ErrUnavailable)
		return
	}
	actor, err := h.Authenticate(r)
	if err != nil {
		writeFailure(w, r, err)
		return
	}
	if r.URL.Path == "/api/v1/platform/eligibility-check" {
		h.serveCheck(w, r, actor)
		return
	}
	if h.Authorize == nil {
		writeFailure(w, r, e.ErrUnavailable)
		return
	}
	school, action := r.PathValue("school"), r.PathValue("action")
	if !e.ValidSchool(school) || (action != "" && !e.ValidAction(action)) {
		writeFailure(w, r, e.ErrRule)
		return
	}
	allowed := "GET, HEAD"
	if action != "" {
		allowed += ", PUT, DELETE"
	}
	if r.Method != "GET" && r.Method != "HEAD" && (action == "" || r.Method != "PUT" && r.Method != "DELETE") {
		w.Header().Set("Allow", allowed)
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "method_not_allowed", "request_id": transport.RequestID(r.Context())})
		return
	}
	if err = h.Authorize(r.Context(), actor, school); err != nil {
		writeFailure(w, r, err)
		return
	}
	if action == "" {
		cfg, getErr := h.Service.GetSchool(r.Context(), school)
		if getErr != nil {
			writeFailure(w, r, getErr)
			return
		}
		if r.Method == "HEAD" {
			w.WriteHeader(http.StatusOK)
			return
		}
		_ = json.NewEncoder(w).Encode(cfg)
		return
	}
	var result e.Rule
	switch r.Method {
	case "GET", "HEAD":
		result, err = h.Service.Get(r.Context(), school, action)
	case "PUT":
		var in e.Change
		if err = decodeIdempotentJSON(w, r, &in); err != nil {
			writeFailure(w, r, err)
			return
		}
		result, err = h.Service.Set(r.Context(), actor, r.Header.Get("Idempotency-Key"), school, action, in)
	case "DELETE":
		var in e.Reset
		if err = decodeIdempotentJSON(w, r, &in); err != nil {
			writeFailure(w, r, err)
			return
		}
		result, err = h.Service.Reset(r.Context(), actor, r.Header.Get("Idempotency-Key"), school, action, in)
	}
	if err != nil {
		writeFailure(w, r, err)
		return
	}
	if r.Method == "HEAD" {
		w.WriteHeader(http.StatusOK)
		return
	}
	_ = json.NewEncoder(w).Encode(result)
}
