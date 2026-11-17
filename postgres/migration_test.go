package postgres_test

import (
	"context"
	"testing"
	"time"

	p "github.com/StuHelper/stuhelper-platform/postgres"
)

func TestIncrementalMigrationSkipsAppliedVersionsWithoutAuditTableLock(t *testing.T) {
	repo, owner := setup(t)

	var appliedCount int
	if err := owner.QueryRow(`SELECT count(*) FROM platform.schema_migrations`).Scan(&appliedCount); err != nil || appliedCount != 4 {
		t.Fatalf("expected 4 applied migrations, got %d (err=%v)", appliedCount, err)
	}

	// Hold an active ACCESS SHARE lock on platform.audit via a runtime read transaction.
	// Re-running ALTER TABLE platform.audit would block waiting for ACCESS EXCLUSIVE;
	// versioned Migrate skips already-applied versions and finishes immediately.
	readTx, err := repo.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer readTx.Rollback()

	var auditCount int
	if err = readTx.QueryRowContext(ctx, `SELECT count(*) FROM platform.audit`).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}

	migCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	if err = p.Migrate(migCtx, owner); err != nil {
		t.Fatalf("incremental Migrate blocked or failed while platform.audit was read-locked: %v", err)
	}
}
