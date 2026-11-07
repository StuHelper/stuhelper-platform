package filerepo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	e "github.com/StuHelper/stuhelper-platform/eligibility"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

func digest(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func (r Repository) Apply(ctx context.Context, in e.Update) (e.Rule, error) {
	lock, err := os.OpenFile(r.Path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return e.Rule{}, e.ErrUnavailable
	}
	defer lock.Close()
	for {
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if err != syscall.EWOULDBLOCK {
			return e.Rule{}, e.ErrUnavailable
		}
		select {
		case <-ctx.Done():
			return e.Rule{}, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	v, err := r.Read(ctx)
	if err != nil {
		return e.Rule{}, err
	}
	key := digest([]string{in.Actor, in.Key})
	hash := digest([]any{in.School, in.Action, in.Change})
	receipts := []receipt{}
	for _, receipt := range v.Receipts {
		if !in.At.Before(receipt.At.Add(24 * time.Hour)) {
			continue
		}
		if receipt.Key == key {
			if receipt.Digest != hash {
				return e.Rule{}, e.ErrConflict
			}
			return receipt.Result, nil
		}
		receipts = append(receipts, receipt)
	}
	if v.Version != in.Change.ExpectedVersion {
		return e.Rule{}, e.ErrConflict
	}
	audits := []audit{}
	for _, event := range v.Audit {
		if event.At.After(in.At.AddDate(-1, 0, 0)) {
			audits = append(audits, event)
		}
	}
	if len(receipts) >= 128 || len(audits) >= 512 {
		return e.Rule{}, e.ErrUnavailable
	}
	version := digest([]any{v.Version, key, hash, in.At})
	result := e.Rule{Version: version, Conditions: append([]e.Condition{}, in.Change.Conditions...)}
	v.Version = version
	if v.Rules == nil {
		v.Rules = map[string]map[string][]e.Condition{}
	}
	if v.Rules[in.School] == nil {
		v.Rules[in.School] = map[string][]e.Condition{}
	}
	v.Rules[in.School][in.Action] = result.Conditions
	v.Receipts = append(receipts, receipt{key, hash, result, in.At})
	v.Audit = append(audits, audit{in.Actor, in.School, in.Action, version, in.At})
	b, err := json.Marshal(v)
	if err != nil || len(b) > 65536 {
		return e.Rule{}, e.ErrUnavailable
	}
	tmp, err := os.CreateTemp(filepath.Dir(r.Path), ".eligibility-*")
	if err != nil {
		return e.Rule{}, e.ErrUnavailable
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if _, err = tmp.Write(b); err != nil {
		return e.Rule{}, e.ErrUnavailable
	}
	if tmp.Sync() != nil || tmp.Close() != nil {
		return e.Rule{}, e.ErrUnavailable
	}
	if err = ctx.Err(); err != nil {
		return e.Rule{}, err
	}
	if os.Rename(tmp.Name(), r.Path) != nil {
		return e.Rule{}, e.ErrUnavailable
	}
	dir, err := os.Open(filepath.Dir(r.Path))
	if err != nil {
		return e.Rule{}, e.ErrUnavailable
	}
	defer dir.Close()
	if dir.Sync() != nil {
		return e.Rule{}, e.ErrUnavailable
	}
	return result, nil
}
