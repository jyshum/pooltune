BEGIN;
SET LOCAL search_path = pg_catalog, pg_temp;
SET LOCAL lock_timeout = '2s';
SET LOCAL statement_timeout = '10s';

DO $discovery$
BEGIN
    IF to_regprocedure('pooltune_monitor_role.snapshot()') IS NULL THEN
        RAISE EXCEPTION 'install the Pooltune monitor before enabling automatic discovery';
    END IF;
END
$discovery$;

-- POOLTUNE_SNAPSHOT

COMMIT;
