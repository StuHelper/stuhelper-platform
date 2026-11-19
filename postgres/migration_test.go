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
	if err := owner.QueryRow(`SELECT count(*) FROM platform.schema_migrations`).Scan(&appliedCount); err != nil || appliedCount != 6 {
		t.Fatalf("expected 6 applied migrations, got %d (err=%v)", appliedCount, err)
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

func TestIncrementalMigrationPreservesExistingSchoolRevisions(t *testing.T) {
	_, owner := setup(t)
	// Recreate the pre-v5 rows in this test's isolated database.
	if _, err := owner.Exec(`DELETE FROM platform.schema_migrations WHERE version=5;
DROP TABLE platform.eligibility_revisions;
ALTER TABLE platform.admin_roles DROP COLUMN created_at;
UPDATE platform.configuration SET revision=7 WHERE singleton;
INSERT INTO platform.eligibility_rules(school,action,conditions) VALUES ('existing-school','content.enter','["content_access"]');
INSERT INTO platform.admin_roles(id,definition) VALUES ('existing-role','{}')`); err != nil {
		t.Fatal(err)
	}
	if err := p.Migrate(ctx, owner); err != nil {
		t.Fatal(err)
	}
	var revision int
	var present bool
	if err := owner.QueryRow(`SELECT revision, EXISTS(SELECT 1 FROM platform.admin_roles WHERE id='existing-role' AND created_at IS NOT NULL)
FROM platform.eligibility_revisions WHERE school='existing-school'`).Scan(&revision, &present); err != nil || revision != 7 || !present {
		t.Fatalf("upgraded data: revision=%d role=%t error=%v", revision, present, err)
	}
}

func TestResetInvalidRoleCapabilities(t *testing.T) {
	_, owner := setup(t)
	if _, err := owner.Exec(`DELETE FROM platform.schema_migrations WHERE version=6;
 INSERT INTO platform.admin_roles(id,definition) VALUES
 ('mixed','{"capabilities":["a..b","resources.manage"]}'),
 ('obsolete','{"capabilities":["legacy"]}'),('valid','{"capabilities":["catalog.maintain"]}');
 INSERT INTO platform.admin_subjects(subject,revision) VALUES ('affected',7),('unaffected',2);
 INSERT INTO platform.admin_grants VALUES ('affected','mixed','*','*','*'),('affected','obsolete','*','*','*'),('unaffected','valid','*','*','*');
 INSERT INTO platform.admin_requests VALUES ('actor','key','previous','{"capabilities":["a..b"]}',now());`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := p.Migrate(ctx, owner); err != nil {
			t.Fatal(err)
		}
	}
	var ok bool
	if err := owner.QueryRow(`SELECT
 (SELECT definition->'capabilities' FROM platform.admin_roles WHERE id='mixed')='["resources.manage"]'
 AND NOT EXISTS (SELECT 1 FROM platform.admin_roles WHERE id='obsolete')
 AND (SELECT count(*) FROM platform.admin_grants)=2
 AND (SELECT revision FROM platform.admin_subjects WHERE subject='affected')=8
 AND (SELECT revision FROM platform.admin_subjects WHERE subject='unaffected')=2
 AND (SELECT result='{}' AND digest <> 'previous' FROM platform.admin_requests WHERE key='key')`).Scan(&ok); err != nil || !ok {
		t.Fatalf("invalid role reset: ok=%v err=%v", ok, err)
	}
}
