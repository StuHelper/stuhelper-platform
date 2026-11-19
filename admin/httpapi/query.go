package httpapi

import (
	"net/http"
	"net/url"
	"slices"

	a "github.com/StuHelper/stuhelper-platform/admin"
)

var optionalStringParams = []string{"school", "group", "module", "actor", "target", "action", "cursor"}

func parseQuery(r *http.Request) (url.Values, error) {
	q, e := url.ParseQuery(r.URL.RawQuery)
	if e != nil {
		return nil, a.ErrInvalid
	}
	isRead := r.Method == "GET" || r.Method == "HEAD"
	allowed := []string{}
	switch {
	case r.URL.Path == "/api/v1/platform/admin/operations" && isRead:
		allowed = []string{"school", "group", "module", "actor", "target", "action", "from", "until", "cursor", "limit", "sources"}
	case r.URL.Path == "/api/v1/platform/admin/roles" && isRead:
		allowed = []string{"keyword", "capability", "cursor", "limit"}
	case r.URL.Path == "/api/v1/platform/admin/administrators" && isRead:
		allowed = []string{"school", "group", "module", "role_id", "capability", "cursor", "limit"}
	case r.PathValue("subject") != "" && isRead:
		allowed = []string{"school", "group", "module"}
	}
	for k, v := range q {
		if !slices.Contains(allowed, k) || len(v) != 1 {
			return nil, a.ErrInvalid
		}
		if v[0] == "" {
			if slices.Contains(optionalStringParams, k) {
				q.Del(k)
				continue
			}
			return nil, a.ErrInvalid
		}
	}
	return q, nil
}
