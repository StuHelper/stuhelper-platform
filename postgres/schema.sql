-- +migration 1
CREATE SCHEMA IF NOT EXISTS platform;
CREATE TABLE IF NOT EXISTS platform.schema_migrations(version integer PRIMARY KEY CHECK(version>0), applied_at timestamptz NOT NULL DEFAULT now());
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

-- +migration 2
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

-- +migration 3
ALTER TABLE platform.audit ADD COLUMN IF NOT EXISTS recorded_xid xid8 NOT NULL DEFAULT pg_current_xact_id();
CREATE TABLE IF NOT EXISTS platform.audit_retention(singleton boolean PRIMARY KEY CHECK(singleton),revision bigint NOT NULL);
INSERT INTO platform.audit_retention VALUES(true,1) ON CONFLICT DO NOTHING;
CREATE INDEX IF NOT EXISTS platform_audit_merge ON platform.audit(created_at DESC,(id::text) COLLATE "C" DESC);
REVOKE ALL ON ALL TABLES IN SCHEMA platform FROM PUBLIC;

-- +migration 4
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

-- +migration 5
CREATE TABLE IF NOT EXISTS platform.eligibility_revisions(school text PRIMARY KEY, revision bigint NOT NULL CHECK(revision>0));
INSERT INTO platform.eligibility_revisions(school,revision) SELECT '*',revision FROM platform.configuration WHERE singleton ON CONFLICT(school) DO NOTHING;
INSERT INTO platform.eligibility_revisions(school,revision) SELECT DISTINCT r.school,c.revision FROM platform.eligibility_rules r CROSS JOIN platform.configuration c WHERE c.singleton ON CONFLICT(school) DO NOTHING;
ALTER TABLE platform.admin_roles ADD COLUMN IF NOT EXISTS created_at timestamptz NOT NULL DEFAULT now();
CREATE INDEX IF NOT EXISTS platform_admin_roles_order ON platform.admin_roles(created_at DESC, id COLLATE "C" DESC);
CREATE INDEX IF NOT EXISTS platform_admin_grants_scope ON platform.admin_grants(school, group_id, module, subject);
CREATE INDEX IF NOT EXISTS platform_audit_scope_merge ON platform.audit(school,group_id,module,created_at DESC,(id::text) COLLATE "C" DESC);
CREATE INDEX IF NOT EXISTS platform_audit_actor_merge ON platform.audit(actor,created_at DESC,(id::text) COLLATE "C" DESC);
CREATE INDEX IF NOT EXISTS platform_audit_target_merge ON platform.audit(target,created_at DESC,(id::text) COLLATE "C" DESC);
CREATE INDEX IF NOT EXISTS platform_audit_action_merge ON platform.audit(action,created_at DESC,(id::text) COLLATE "C" DESC);

-- +migration 6
-- Invalid strings have no unambiguous mapping to an authorization capability.
CREATE TEMP TABLE invalid_role_capabilities ON COMMIT DROP AS
SELECT r.id, coalesce(jsonb_agg(cap ORDER BY ord) FILTER (WHERE valid), '[]'::jsonb) AS remaining
FROM platform.admin_roles r
CROSS JOIN LATERAL jsonb_array_elements_text(r.definition->'capabilities') WITH ORDINALITY AS c(cap,ord)
CROSS JOIN LATERAL (SELECT length(cap) <= 128 AND cap COLLATE "C" ~ '^[a-z][a-z0-9-]*([.][a-z][a-z0-9_-]*)+$' AS valid) v
GROUP BY r.id HAVING NOT bool_and(valid);

UPDATE platform.admin_subjects s SET revision = revision + 1
WHERE EXISTS (SELECT 1 FROM platform.admin_grants g JOIN invalid_role_capabilities i ON i.id=g.role_id WHERE g.subject=s.subject);
DELETE FROM platform.admin_grants g USING invalid_role_capabilities i WHERE g.role_id=i.id AND i.remaining='[]';
DELETE FROM platform.admin_roles r USING invalid_role_capabilities i WHERE r.id=i.id AND i.remaining='[]';
UPDATE platform.admin_roles r SET definition=jsonb_set(definition,'{capabilities}',i.remaining)
FROM invalid_role_capabilities i WHERE r.id=i.id;

-- Keep the keys reserved until expiry. A retry must never return an obsolete role.
UPDATE platform.admin_requests SET digest='invalidated-role-format', result='{}'
WHERE EXISTS (SELECT 1 FROM jsonb_path_query(result,'strict $.**.capabilities[*]', '{}', true) cap
              WHERE length(cap #>> '{}') > 128 OR (cap #>> '{}') COLLATE "C" !~ '^[a-z][a-z0-9-]*([.][a-z][a-z0-9_-]*)+$');
