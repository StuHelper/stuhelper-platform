package filerepo_test

import (
	"context"
	"errors"
	"fmt"
	e "github.com/StuHelper/stuhelper-platform/eligibility"
	"github.com/StuHelper/stuhelper-platform/eligibility/filerepo"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRuleUpdatesAreVersionedIdempotentAndIndependent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules.json")
	if err := os.WriteFile(path, []byte(`{"version":"initial","rules":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	repo := filerepo.Repository{Path: path}
	s := e.Service{Repo: repo, Now: func() time.Time { return time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC) }}
	ctx := context.Background()
	in := e.Change{ExpectedVersion: "initial", Conditions: []e.Condition{e.Content, e.Phone}}
	first, err := s.Set(ctx, "admin", "write", "school", "content.enter", in)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		v, err := s.Set(ctx, "admin", "write", "school", "content.enter", in)
		if err != nil || v.Version != first.Version {
			t.Fatal(v, err)
		}
	}
	in.Conditions = []e.Condition{}
	if _, err = s.Set(ctx, "admin", "write", "school", "content.enter", in); !errors.Is(err, e.ErrConflict) {
		t.Fatal("changed replay accepted", err)
	}
	read, err := repo.Effective(ctx, "read_full", "school")
	if err != nil || len(read.Conditions) != 1 || read.Conditions[0] != e.Content {
		t.Fatal("entry rule changed review rule", read, err)
	}
	var won atomic.Int32
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			_, err := s.Set(ctx, "admin", fmt.Sprint(i), "school", "content.enter", e.Change{ExpectedVersion: first.Version, Conditions: []e.Condition{e.Content}})
			if err == nil {
				won.Add(1)
			} else if !errors.Is(err, e.ErrConflict) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if won.Load() != 1 {
		t.Fatal("concurrent revision lost", won.Load())
	}
	if err = os.WriteFile(path, []byte(`{"version":"bad","rules":{"school":{"content.enter":["unknown_fact"]}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.Effective(ctx, "content.enter", "school"); err == nil {
		t.Fatal("corrupt config fell back to permissive rule")
	}
}
