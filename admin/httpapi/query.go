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
	switch {
	case r.URL.Path == "/api/v1/platform/admin/operations":
		allowed = []string{"school", "group", "module", "actor", "target", "action", "from", "until", "cursor", "limit", "sources"}
	case r.URL.Path == "/api/v1/platform/admin/roles" && r.Method == "GET":
		allowed = []string{"keyword", "capability", "cursor", "limit"}
	case r.URL.Path == "/api/v1/platform/admin/administrators" && r.Method == "GET":
		allowed = []string{"school", "group", "module", "role_id", "capability", "cursor", "limit"}
	case r.PathValue("subject") != "" && r.Method == "GET":
		allowed = []string{"school", "group", "module"}
	}
	for k, v := range q {
		if !slices.Contains(allowed, k) || len(v) != 1 || v[0] == "" {
			return nil, a.ErrInvalid
		}
	}
	return q, nil
}
