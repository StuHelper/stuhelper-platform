// Package httpapi exposes platform administration through an authenticated host.
package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/StuHelper/stuhelper-platform/account"
	a "github.com/StuHelper/stuhelper-platform/admin"
)

type Handler struct {
	Service a.Service
	// Authenticate must validate the current session and CSRF for writes.
	Authenticate func(*http.Request) (account.Principal, error)
}

// Register mounts only platform-owned endpoints. Hosts supply trusted identity
// and session/CSRF adapters; public examples do not create demo administrators.
func (h Handler) Register(mux *http.ServeMux) {
	for _, pattern := range []string{"GET /role-templates", "POST /roles", "GET /roles/{role}", "GET /administrators/{subject}/effective", "PUT /administrators/{subject}/grants", "DELETE /administrators/{subject}/grants", "GET /operations"} {
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
func decode(w http.ResponseWriter, r *http.Request, out any) error {
	if len(r.Header.Values("Idempotency-Key")) != 1 {
		return a.ErrInvalid
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32768))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil || d.Decode(new(any)) != io.EOF {
		return a.ErrInvalid
	}
	return nil
}
func failure(w http.ResponseWriter, err error) {
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
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code})
}
func (h Handler) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if h.Authenticate == nil {
		failure(w, a.ErrUnavailable)
		return
	}
	p, err := h.Authenticate(r)
	if err != nil {
		failure(w, err)
		return
	}
	q, err := parseQuery(r)
	if err != nil {
		failure(w, err)
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
	case r.Method == "POST":
		var in a.RoleInput
		if err = decode(w, r, &in); err == nil {
			out, err = h.Service.CreateRole(r.Context(), p, r.Header.Get("Idempotency-Key"), in)
		}
	case r.PathValue("role") != "":
		out, err = h.Service.Role(r.Context(), p, r.PathValue("role"))
	case r.Method == "PUT" || r.Method == "DELETE":
		var in a.Mutation
		if err = decode(w, r, &in); err == nil {
			out, err = h.Service.Change(r.Context(), p, r.PathValue("subject"), r.Header.Get("Idempotency-Key"), in, r.Method == "DELETE")
		}
	case r.PathValue("subject") != "":
		out, err = h.Service.Effective(r.Context(), p, r.PathValue("subject"), scope)
	default:
		f := a.Filter{Scope: scope, Actor: q.Get("actor"), Target: q.Get("target"), Action: q.Get("action"), Cursor: q.Get("cursor"), Limit: 50}
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
		failure(w, err)
		return
	}
	_ = json.NewEncoder(w).Encode(out)
}
