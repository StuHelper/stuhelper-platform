package filerepo_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	h "github.com/StuHelper/stuhelper-platform/modulehistory"
	"github.com/StuHelper/stuhelper-platform/modulehistory/filerepo"
)

func TestInstallationHistoryCannotTreatDisabledOrUnknownAsNeverInstalled(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "history.json")
	r := filerepo.Repository{Path: path}
	if state, err := r.State(ctx, "reviews"); err == nil || state != h.Unknown {
		t.Fatal(state, err)
	}
	if err := filerepo.Initialize(path, []string{"reviews", "resources"}); err != nil {
		t.Fatal(err)
	}
	if state, err := r.State(ctx, "reviews"); err != nil || state != h.NeverInstalled {
		t.Fatal(state, err)
	}
	if state, err := r.State(ctx, "favorites"); err != nil || state != h.Unknown {
		t.Fatal(state, err)
	}
	if err := r.ObserveInstalled(ctx, []string{"reviews"}); err != nil {
		t.Fatal(err)
	}
	if err := filerepo.Initialize(path, []string{"reviews"}); err == nil {
		t.Fatal("history reset accepted")
	}
	// A new process enabling fewer modules must retain the original installation.
	r = filerepo.Repository{Path: path}
	if err := r.ObserveInstalled(ctx, []string{"platform"}); err != nil {
		t.Fatal(err)
	}
	if state, err := r.State(ctx, "reviews"); err != nil || state != h.Installed {
		t.Fatal(state, err)
	}
	if state, err := r.State(ctx, "resources"); err != nil || state != h.NeverInstalled {
		t.Fatal(state, err)
	}
	if err := os.WriteFile(path, []byte(`{"version":1`), 0600); err != nil {
		t.Fatal(err)
	}
	if state, err := r.State(ctx, "resources"); !errors.Is(err, h.ErrUnavailable) || state != h.Unknown {
		t.Fatal("corruption certifies absence", state, err)
	}
}
