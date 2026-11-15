BEGIN;
SET LOCAL search_path = pg_catalog, pg_temp;
SET LOCAL lock_timeout = '2s';
SET LOCAL statement_timeout = '10s';

CREATE ROLE pooltune_monitor_role NOLOGIN NOINHERIT NOSUPERUSER NOCREATEROLE NOCREATEDB NOBYPASSRLS NOREPLICATION CONNECTION LIMIT 2;
GRANT CONNECT ON DATABASE postgres TO pooltune_monitor_role;
CREATE SCHEMA pooltune_monitor_role;
REVOKE ALL ON SCHEMA pooltune_monitor_role FROM PUBLIC;

CREATE TABLE pooltune_monitor_role.enrolled_queries (
    slot smallint PRIMARY KEY CHECK (slot BETWEEN 1 AND 20),
    role_oid oid NOT NULL CHECK (role_oid <> 0),
    query_id bigint NOT NULL CHECK (query_id <> 0),
    mean_execution_ms double precision NOT NULL
        CHECK (mean_execution_ms > 0 AND mean_execution_ms < 'Infinity'::double precision),
    UNIQUE (role_oid, query_id)
);
REVOKE ALL ON TABLE pooltune_monitor_role.enrolled_queries FROM PUBLIC;

-- POOLTUNE_SNAPSHOT

REVOKE ALL ON FUNCTION pooltune_monitor_role.snapshot() FROM PUBLIC;
GRANT USAGE ON SCHEMA pooltune_monitor_role TO pooltune_monitor_role;
GRANT EXECUTE ON FUNCTION pooltune_monitor_role.snapshot() TO pooltune_monitor_role;

DO $grants$
BEGIN
    -- Refuse project-wide default privileges which would expose these objects.
    IF EXISTS (
        SELECT FROM pg_namespace n, LATERAL aclexplode(n.nspacl) a
        WHERE n.nspname = 'pooltune_monitor_role'
          AND a.grantee NOT IN (n.nspowner, 'pooltune_monitor_role'::regrole)
    ) OR EXISTS (
        SELECT FROM pg_class c, LATERAL aclexplode(c.relacl) a
        WHERE c.oid = 'pooltune_monitor_role.enrolled_queries'::regclass
          AND a.grantee <> c.relowner
    ) OR EXISTS (
        SELECT FROM pg_proc p, LATERAL aclexplode(p.proacl) a
        WHERE p.oid = 'pooltune_monitor_role.snapshot()'::regprocedure
          AND a.grantee NOT IN (p.proowner, 'pooltune_monitor_role'::regrole)
    ) THEN
        RAISE EXCEPTION 'unexpected default privileges on Pooltune monitor objects';
    END IF;
END
$grants$;
COMMIT;
