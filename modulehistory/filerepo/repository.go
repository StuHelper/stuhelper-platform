// Package filerepo stores a private deployment inventory, not business data.
package filerepo

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"regexp"
	"syscall"
	"time"

	h "github.com/StuHelper/stuhelper-platform/modulehistory"
)

type Repository struct{ Path string }
type inventory struct {
	Version int                `json:"version"`
	Modules map[string]h.State `json:"modules"`
}

var identifier = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

// Initialize uses exclusive creation: existing installation history cannot be reset.
// The operator supplies only modules whose absence throughout deployment is known.
func Initialize(path string, never []string) error {
	v := inventory{1, map[string]h.State{}}
	for _, name := range never {
		if !identifier.MatchString(name) || v.Modules[name] != "" {
			return h.ErrUnavailable
		}
		v.Modules[name] = h.NeverInstalled
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return h.ErrUnavailable
	}
	defer f.Close()
	if json.NewEncoder(f).Encode(v) != nil || f.Sync() != nil {
		return h.ErrUnavailable
	}
	return nil
}

func (r Repository) access(ctx context.Context, update func(*inventory) error, write bool) error {
	f, err := os.OpenFile(r.Path, os.O_RDWR, 0)
	if err != nil {
		return h.ErrUnavailable
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 65536 {
		return h.ErrUnavailable
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if err != syscall.EWOULDBLOCK {
			return h.ErrUnavailable
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	if err := ctx.Err(); err != nil {
		return err
	}
	var v inventory
	d := json.NewDecoder(io.LimitReader(f, 65537))
	d.DisallowUnknownFields()
	if d.Decode(&v) != nil || d.Decode(new(any)) != io.EOF || v.Version != 1 || v.Modules == nil || len(v.Modules) > 200 {
		return h.ErrUnavailable
	}
	for name, state := range v.Modules {
		if !identifier.MatchString(name) || state != h.Installed && state != h.NeverInstalled {
			return h.ErrUnavailable
		}
	}
	if err = update(&v); err != nil {
		return err
	}
	if !write {
		return nil
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return h.ErrUnavailable
	}
	if err = f.Truncate(0); err != nil {
		return h.ErrUnavailable
	}
	// An interrupted write makes history unavailable; it never certifies absence.
	if json.NewEncoder(f).Encode(v) != nil || f.Sync() != nil {
		return h.ErrUnavailable
	}
	return nil
}
func (r Repository) State(ctx context.Context, name string) (h.State, error) {
	state := h.Unknown
	err := r.access(ctx, func(v *inventory) error {
		if s, ok := v.Modules[name]; ok {
			state = s
		}
		return nil
	}, false)
	return state, err
}
func (r Repository) ObserveInstalled(ctx context.Context, names []string) error {
	return r.access(ctx, func(v *inventory) error {
		for _, name := range names {
			if !identifier.MatchString(name) {
				return h.ErrUnavailable
			}
			v.Modules[name] = h.Installed
		}
		if len(v.Modules) > 200 {
			return h.ErrUnavailable
		}
		return nil
	}, true)
}
