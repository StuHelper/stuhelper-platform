package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	e "github.com/StuHelper/stuhelper-platform/eligibility"
	"io"
	"net/http"
)

var ErrUnauthenticated = errors.New("unauthenticated")

type Handler struct {
	Service      e.Service
	Authenticate func(*http.Request) (string, error)
	Authorize    func(context.Context, string, string) error
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	fail := func(err error) {
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
			fail(e.ErrRule)
			return
		}
		result, err = h.Service.Set(r.Context(), actor, r.Header.Get("Idempotency-Key"), school, action, in)
	default:
		w.WriteHeader(405)
		return
	}
	if err != nil {
		fail(err)
		return
	}
	_ = json.NewEncoder(w).Encode(result)
}
