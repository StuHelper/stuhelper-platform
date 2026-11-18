// Package httpapi exposes platform administration through an authenticated host.
package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/StuHelper/stuhelper-platform/account"
	a "github.com/StuHelper/stuhelper-platform/admin"
	"github.com/StuHelper/stuhelper-platform/transport"
)

var routePatterns = []string{
	"GET /role-templates",
	"GET /roles",
	"POST /roles",
	"GET /roles/{role}",
	"GET /administrators",
	"GET /administrators/{subject}/effective",
	"PUT /administrators/{subject}/grants",
	"DELETE /administrators/{subject}/grants",
	"POST /administrators/{subject}/grant-changes",
	"POST /batch-grant-changes",
	"GET /operations",
}

type Handler struct {
	Service a.Service
	// Authenticate must validate the current session and CSRF for writes.
	Authenticate func(*http.Request) (account.Principal, error)
}

// NewRoutes adapts a transport.Authenticator using account.Principal and exports
// transport.Route entries ready for application.New.
func NewRoutes(service a.Service, authn transport.Authenticator) []transport.Route {
	return Handler{
		Service: service,
		Authenticate: func(r *http.Request) (account.Principal, error) {
			if authn == nil {
				return account.Principal{}, a.ErrUnavailable
			}
			p, err := authn.Authenticate(r)
			if errors.Is(err, transport.ErrUnauthenticated) {
				return account.Principal{}, a.ErrUnauthenticated
			}
			if errors.Is(err, transport.ErrForbidden) {
				return account.Principal{}, a.ErrForbidden
			}
			if err != nil {
				return account.Principal{}, a.ErrUnavailable
			}
			if !p.Valid() {
				return account.Principal{}, a.ErrUnauthenticated
			}
			return p, nil
		},
	}.Routes()
}

// Routes mounts all platform administration endpoints onto transport.Route descriptors.
// Per-endpoint capability and scope checks are enforced inside Handler.
func (h Handler) Routes() []transport.Route {
	mux := http.NewServeMux()
	h.Register(mux)
	out := make([]transport.Route, 0, len(routePatterns))
	for _, pattern := range routePatterns {
		method, path := split(pattern)
		out = append(out, transport.Route{
			Method:     method,
			Path:       "/api/v1/platform/admin" + path,
			Module:     "platform",
			Capability: "platform.modules",
			Access:     transport.Public,
			Handler:    transport.WithUnavailableCode(mux, a.ErrUnavailable.Error()),
		})
	}
	return out
}

// Register mounts only platform-owned endpoints. Hosts supply trusted identity
// and session/CSRF adapters; public examples do not create demo administrators.
func (h Handler) Register(mux *http.ServeMux) {
	for _, pattern := range routePatterns {
		method, path := split(pattern)
		mux.HandleFunc(method+" /api/v1/platform/admin"+path, h.serve)
	}
}
func split(s string) (string, string) {
	for i, c := range s {
		if c == ' ' {
			return s[:i], s[i+1:]
		}
	}
	return s, ""
}
func decodeBody(w http.ResponseWriter, r *http.Request, out any, maxBytes int64) error {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBytes))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil || d.Decode(new(any)) != io.EOF {
		return a.ErrInvalid
	}
	return nil
}
func decode(w http.ResponseWriter, r *http.Request, out any) error {
	if len(r.Header.Values("Idempotency-Key")) != 1 {
		return a.ErrInvalid
	}
	return decodeBody(w, r, out, 32768)
}
func failure(w http.ResponseWriter, requestID string, err error) {
	code, status := "unavailable", 503
	for _, v := range []struct {
		err    error
		status int
	}{{a.ErrInvalid, 400}, {a.ErrUnauthenticated, 401}, {a.ErrForbidden, 403}, {a.ErrNotFound, 404}, {a.ErrConflict, 409}} {
		if errors.Is(err, v.err) {
			code, status = v.err.Error(), v.status
			break
		}
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "request_id": requestID})
}
func (h Handler) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	var reqID string
	var idErr error
	r, reqID, idErr = transport.EnsureRequestID(w, r)
	if idErr != nil {
		failure(w, "", a.ErrUnavailable)
		return
	}
	if h.Authenticate == nil {
		failure(w, reqID, a.ErrUnavailable)
		return
	}
	p, err := h.Authenticate(r)
	if err != nil {
		failure(w, reqID, err)
		return
	}
	q, err := parseQuery(r)
	if err != nil {
		failure(w, reqID, err)
		return
	}
	scope := a.Scope{School: q.Get("school"), Group: q.Get("group"), Module: q.Get("module")}
	if scope.School == "" {
		scope.School = "*"
	}
	if scope.Group == "" {
		scope.Group = "*"
	}
	if scope.Module == "" {
		scope.Module = "*"
	}
	var out any
	switch {
	case r.URL.Path == "/api/v1/platform/admin/role-templates":
		// Templates disclose only public suggestions, but still require a live identity.
		_, err = h.Service.Effective(r.Context(), p, p.Key(), a.Global)
		out = a.Templates()
	case r.URL.Path == "/api/v1/platform/admin/roles" && r.Method == "GET":
		rf := a.RoleFilter{Keyword: q.Get("keyword"), Capability: q.Get("capability"), Cursor: q.Get("cursor"), Limit: 50}
		if q.Has("limit") {
			rf.Limit, err = strconv.Atoi(q.Get("limit"))
		}
		if err != nil {
			err = a.ErrInvalid
		} else {
			out, err = h.Service.ListRoles(r.Context(), p, rf)
		}
	case r.URL.Path == "/api/v1/platform/admin/roles" && r.Method == "POST":
		var in a.RoleInput
		if err = decode(w, r, &in); err == nil {
			out, err = h.Service.CreateRole(r.Context(), p, r.Header.Get("Idempotency-Key"), in)
		}
	case r.PathValue("role") != "":
		out, err = h.Service.Role(r.Context(), p, r.PathValue("role"))
	case r.URL.Path == "/api/v1/platform/admin/administrators" && r.Method == "GET":
		af := a.AdministratorFilter{Scope: scope, RoleID: q.Get("role_id"), Capability: q.Get("capability"), Cursor: q.Get("cursor"), Limit: 50}
		if q.Has("limit") {
			af.Limit, err = strconv.Atoi(q.Get("limit"))
		}
		if err != nil {
			err = a.ErrInvalid
		} else {
			out, err = h.Service.ListAdministrators(r.Context(), p, af)
		}
	case r.URL.Path == "/api/v1/platform/admin/batch-grant-changes" && r.Method == "POST":
		keys := r.Header.Values("Idempotency-Key")
		if len(keys) > 1 {
			err = a.ErrInvalid
		} else {
			var wire struct {
				DryRun *bool                    `json:"dry_run"`
				Reason string                   `json:"reason"`
				Items  []a.BatchGrantChangeItem `json:"items"`
			}
			if err = decodeBody(w, r, &wire, 131072); err == nil {
				if wire.DryRun == nil || (!*wire.DryRun && len(keys) != 1) {
					err = a.ErrInvalid
				} else {
					in := a.BatchGrantChangesInput{DryRun: *wire.DryRun, Reason: wire.Reason, Items: wire.Items}
					out, err = h.Service.BatchChangeGrants(r.Context(), p, r.Header.Get("Idempotency-Key"), in)
				}
			}
		}
	case r.Method == "POST" && r.PathValue("subject") != "":
		var in a.GrantChangesInput
		if err = decode(w, r, &in); err == nil {
			out, err = h.Service.ChangeGrants(r.Context(), p, r.PathValue("subject"), r.Header.Get("Idempotency-Key"), in)
		}
	case r.Method == "PUT" || r.Method == "DELETE":
		var in a.Mutation
		if err = decode(w, r, &in); err == nil {
			out, err = h.Service.Change(r.Context(), p, r.PathValue("subject"), r.Header.Get("Idempotency-Key"), in, r.Method == "DELETE")
		}
	case r.PathValue("subject") != "":
		out, err = h.Service.Effective(r.Context(), p, r.PathValue("subject"), scope)
	default:
		f := a.Filter{Scope: scope, Actor: q.Get("actor"), Target: q.Get("target"), Action: q.Get("action"), Cursor: q.Get("cursor"), Limit: 50}
		if q.Has("sources") {
			f.Sources = strings.Split(q.Get("sources"), ",")
		}
		if q.Has("limit") {
			f.Limit, err = strconv.Atoi(q.Get("limit"))
		}
		if err == nil && q.Has("from") {
			f.From, err = time.Parse(time.RFC3339Nano, q.Get("from"))
		}
		if err == nil && q.Has("until") {
			f.Until, err = time.Parse(time.RFC3339Nano, q.Get("until"))
		}
		if err != nil {
			err = a.ErrInvalid
		} else {
			out, err = h.Service.Operations(r.Context(), p, f)
		}
	}
	if err != nil {
		failure(w, reqID, err)
		return
	}
	_ = json.NewEncoder(w).Encode(out)
}
