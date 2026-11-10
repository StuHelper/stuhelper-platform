CREATE SCHEMA IF NOT EXISTS platform;
CREATE TABLE IF NOT EXISTS platform.configuration(singleton boolean PRIMARY KEY CHECK(singleton), revision bigint NOT NULL CHECK(revision>0));
INSERT INTO platform.configuration VALUES(true,1) ON CONFLICT DO NOTHING;
CREATE TABLE IF NOT EXISTS platform.eligibility_rules(school text NOT NULL, action text NOT NULL, conditions jsonb NOT NULL CHECK(jsonb_typeof(conditions)='array'), PRIMARY KEY(school,action));
CREATE TABLE IF NOT EXISTS platform.eligibility_requests(actor text NOT NULL, key text NOT NULL, digest text NOT NULL, result jsonb NOT NULL, created_at timestamptz NOT NULL, PRIMARY KEY(actor,key));
CREATE INDEX IF NOT EXISTS platform_requests_expiry ON platform.eligibility_requests(created_at);
CREATE TABLE IF NOT EXISTS platform.installations(module text PRIMARY KEY, state text NOT NULL CHECK(state IN('installed','never_installed')));
CREATE TABLE IF NOT EXISTS platform.audit(id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, actor text NOT NULL, action text NOT NULL, target text NOT NULL, data jsonb NOT NULL, created_at timestamptz NOT NULL);
CREATE INDEX IF NOT EXISTS platform_audit_expiry ON platform.audit(created_at);
REVOKE ALL ON SCHEMA platform FROM PUBLIC;
REVOKE ALL ON ALL TABLES IN SCHEMA platform FROM PUBLIC;

CREATE TABLE IF NOT EXISTS platform.admin_roles(id text PRIMARY KEY, definition jsonb NOT NULL);
CREATE TABLE IF NOT EXISTS platform.admin_subjects(subject text PRIMARY KEY, revision bigint NOT NULL DEFAULT 0 CHECK(revision>=0));
CREATE TABLE IF NOT EXISTS platform.admin_grants(subject text NOT NULL REFERENCES platform.admin_subjects(subject), role_id text NOT NULL REFERENCES platform.admin_roles(id), school text NOT NULL, group_id text NOT NULL, module text NOT NULL, PRIMARY KEY(subject,role_id,school,group_id,module));
CREATE TABLE IF NOT EXISTS platform.admin_requests(actor text NOT NULL,key text NOT NULL,digest text NOT NULL,result jsonb NOT NULL,created_at timestamptz NOT NULL,PRIMARY KEY(actor,key));
CREATE INDEX IF NOT EXISTS platform_admin_requests_expiry ON platform.admin_requests(created_at);
ALTER TABLE platform.audit ADD COLUMN IF NOT EXISTS school text NOT NULL DEFAULT '*';
ALTER TABLE platform.audit ADD COLUMN IF NOT EXISTS group_id text NOT NULL DEFAULT '*';
ALTER TABLE platform.audit ADD COLUMN IF NOT EXISTS module text NOT NULL DEFAULT '*';
ALTER TABLE platform.audit ADD COLUMN IF NOT EXISTS reason text NOT NULL DEFAULT '';
ALTER TABLE platform.audit ADD COLUMN IF NOT EXISTS result text NOT NULL DEFAULT 'success';
CREATE INDEX IF NOT EXISTS platform_audit_scope ON platform.audit(school,group_id,module,id DESC);
CREATE INDEX IF NOT EXISTS platform_audit_actor ON platform.audit(actor,id DESC);
CREATE INDEX IF NOT EXISTS platform_audit_target ON platform.audit(target,id DESC);
CREATE INDEX IF NOT EXISTS platform_audit_action ON platform.audit(action,id DESC);
REVOKE ALL ON ALL TABLES IN SCHEMA platform FROM PUBLIC;

ALTER TABLE platform.audit ADD COLUMN IF NOT EXISTS recorded_xid xid8 NOT NULL DEFAULT pg_current_xact_id();
CREATE TABLE IF NOT EXISTS platform.audit_retention(singleton boolean PRIMARY KEY CHECK(singleton),revision bigint NOT NULL);
INSERT INTO platform.audit_retention VALUES(true,1) ON CONFLICT DO NOTHING;
CREATE INDEX IF NOT EXISTS platform_audit_merge ON platform.audit(created_at DESC,(id::text) COLLATE "C" DESC);
REVOKE ALL ON ALL TABLES IN SCHEMA platform FROM PUBLIC;

-- Bounded visibility cache: slot reuse invalidates old tokens explicitly.
CREATE SEQUENCE IF NOT EXISTS platform.audit_snapshot_slot;
CREATE TABLE IF NOT EXISTS platform.audit_snapshots(
 slot integer PRIMARY KEY CHECK(slot>=0 AND slot<4096),
 token text UNIQUE NOT NULL CHECK(token ~ '^[0-9a-f]{32}$'),
 visibility pg_snapshot NOT NULL CHECK(octet_length(visibility::text)<=1048576),
 epoch bigint NOT NULL,
 created_at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS platform_audit_snapshots_expiry ON platform.audit_snapshots(created_at);
REVOKE ALL ON platform.audit_snapshots FROM PUBLIC;
REVOKE ALL ON SEQUENCE platform.audit_snapshot_slot FROM PUBLIC;
