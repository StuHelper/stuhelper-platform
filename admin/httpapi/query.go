package httpapi

import (
	"net/http"
	"net/url"
	"slices"

	a "github.com/StuHelper/stuhelper-platform/admin"
)

func parseQuery(r *http.Request) (url.Values, error) {
	q, e := url.ParseQuery(r.URL.RawQuery)
	if e != nil {
		return nil, a.ErrInvalid
	}
	allowed := []string{}
	if r.URL.Path == "/api/v1/platform/admin/operations" {
		allowed = []string{"school", "group", "module", "actor", "target", "action", "from", "until", "cursor", "limit", "sources"}
	} else if r.PathValue("subject") != "" && r.Method == "GET" {
		allowed = []string{"school", "group", "module"}
	}
	for k, v := range q {
		if !slices.Contains(allowed, k) || len(v) != 1 || v[0] == "" {
			return nil, a.ErrInvalid
		}
	}
	return q, nil
}
