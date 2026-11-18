package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	e "github.com/StuHelper/stuhelper-platform/eligibility"
)

var ErrUnauthenticated = errors.New("unauthenticated")

type Handler struct {
	Service      e.Service
	Authenticate func(*http.Request) (string, error)
	Authorize    func(context.Context, string, string) error
	// Admission or Admissions supplies runtime fact evaluation for GET /api/v1/platform/eligibility-check.
	Admission  *e.Admission
	Admissions map[string]e.Admission
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

func writeFailure(w http.ResponseWriter, err error) {
	code, status := "unavailable", 503
	switch {
	case errors.Is(err, ErrUnauthenticated):
		code, status = "unauthenticated", 401
	case errors.Is(err, e.ErrForbidden):
		code, status = "forbidden", 403
	case errors.Is(err, e.ErrRule):
		code, status = "invalid_rule", 400
	case errors.Is(err, e.ErrConflict):
		code, status = "conflict", 409
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code})
}

func decodeIdempotentJSON(w http.ResponseWriter, r *http.Request, out any) error {
	if len(r.Header.Values("Idempotency-Key")) != 1 {
		return e.ErrRule
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil || d.Decode(new(any)) != io.EOF {
		return e.ErrRule
	}
	return nil
}

func (h Handler) serveCheck(w http.ResponseWriter, r *http.Request, actor string) {
	if r.Method != "GET" {
		w.Header().Set("Allow", "GET")
		w.WriteHeader(405)
		return
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeFailure(w, e.ErrRule)
		return
	}
	for k, v := range q {
		if (k != "school" && k != "actions") || len(v) != 1 || v[0] == "" {
			writeFailure(w, e.ErrRule)
			return
		}
	}
	school := q.Get("school")
	rawActions := q.Get("actions")
	if !e.ValidSchool(school) || rawActions == "" {
		writeFailure(w, e.ErrRule)
		return
	}
	actions := strings.Split(rawActions, ",")
	adm, ok := h.resolveAdmission(school)
	if !ok {
		// Distinguish cross-school requests against a single configured school from unconfigured preflight.
		if h.Admission != nil && h.Admission.School != "" && school != h.Admission.School {
			writeFailure(w, e.ErrForbidden)
			return
		}
		if len(h.Admissions) > 0 {
			writeFailure(w, e.ErrForbidden)
			return
		}
		writeFailure(w, e.ErrUnavailable)
		return
	}
	batch, err := adm.Preview(r.Context(), actor, school, actions)
	if err != nil {
		writeFailure(w, err)
		return
	}
	_ = json.NewEncoder(w).Encode(batch)
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if h.Authenticate == nil {
		writeFailure(w, e.ErrUnavailable)
		return
	}
	actor, err := h.Authenticate(r)
	if err != nil {
		writeFailure(w, err)
		return
	}
	if r.URL.Path == "/api/v1/platform/eligibility-check" {
		h.serveCheck(w, r, actor)
		return
	}
	if h.Authorize == nil {
		writeFailure(w, e.ErrUnavailable)
		return
	}
	school, action := r.PathValue("school"), r.PathValue("action")
	if !e.ValidSchool(school) || (action != "" && !e.ValidAction(action)) {
		writeFailure(w, e.ErrRule)
		return
	}
	allowed := "GET, HEAD"
	if action != "" {
		allowed += ", PUT, DELETE"
	}
	if r.Method != "GET" && r.Method != "HEAD" && (action == "" || r.Method != "PUT" && r.Method != "DELETE") {
		w.Header().Set("Allow", allowed)
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "method_not_allowed"})
		return
	}
	if err = h.Authorize(r.Context(), actor, school); err != nil {
		writeFailure(w, err)
		return
	}
	if action == "" {
		cfg, getErr := h.Service.GetSchool(r.Context(), school)
		if getErr != nil {
			writeFailure(w, getErr)
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
			writeFailure(w, err)
			return
		}
		result, err = h.Service.Set(r.Context(), actor, r.Header.Get("Idempotency-Key"), school, action, in)
	case "DELETE":
		var in e.Reset
		if err = decodeIdempotentJSON(w, r, &in); err != nil {
			writeFailure(w, err)
			return
		}
		result, err = h.Service.Reset(r.Context(), actor, r.Header.Get("Idempotency-Key"), school, action, in)
	}
	if err != nil {
		writeFailure(w, err)
		return
	}
	if r.Method == "HEAD" {
		w.WriteHeader(http.StatusOK)
		return
	}
	_ = json.NewEncoder(w).Encode(result)
}
