// Package filerepo owns the persisted action-rule configuration.
package filerepo

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"time"

	e "github.com/StuHelper/stuhelper-platform/eligibility"
)

type Repository struct{ Path string }
type receipt struct {
	Key, Digest string
	Result      e.Rule
	At          time.Time
}
type audit struct {
	Actor, School, Action, Version string
	At                             time.Time
}
type document struct {
	e.Configuration
	Receipts []receipt `json:"receipts,omitempty"`
	Audit    []audit   `json:"audit,omitempty"`
}

func (r Repository) Read(context.Context) (document, error) {
	var v document
	info, err := os.Lstat(r.Path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 65536 {
		return v, e.ErrUnavailable
	}
	f, err := os.Open(r.Path)
	if err != nil {
		return v, e.ErrUnavailable
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return v, e.ErrUnavailable
	}
	b, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil || len(b) > 65536 {
		return v, e.ErrUnavailable
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(&v) != nil || d.Decode(new(any)) != io.EOF || v.Version == "" || len(v.Version) > 128 {
		return v, e.ErrUnavailable
	}
	for _, rules := range v.Rules {
		for action, conditions := range rules {
			if conditions == nil {
				return v, e.ErrUnavailable
			}
			if _, err := (e.Defaults{}).Effective(context.Background(), action, ""); err != nil || e.Validate(e.Rule{Version: v.Version, Conditions: conditions}) != nil {
				return v, e.ErrUnavailable
			}
		}
	}
	return v, nil
}
func (r Repository) Effective(ctx context.Context, action, school string) (e.Rule, error) {
	v, err := r.Read(ctx)
	if err != nil {
		return e.Rule{}, err
	}
	for _, scope := range []string{school, "*"} {
		if conditions, ok := v.Rules[scope][action]; ok {
			return e.Rule{Version: v.Version, Conditions: conditions}, nil
		}
	}
	rule, err := (e.Defaults{}).Effective(ctx, action, school)
	rule.Version = v.Version
	return rule, err
}
