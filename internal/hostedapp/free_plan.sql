-- A reservation counts against both budgets until provider usage is known.
-- Rows survive project removal and are never reset at month boundaries.
CREATE TABLE IF NOT EXISTS pooltune_app.free_model_usage (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    owner_id bigint NOT NULL,
    month date NOT NULL,
    credits integer NOT NULL CHECK (credits > 0),
    micros bigint NOT NULL CHECK (micros >= 0),
    settled boolean NOT NULL DEFAULT false
);
CREATE INDEX IF NOT EXISTS free_model_owner_month ON pooltune_app.free_model_usage(owner_id, month);

-- Preserve the recorded pre-rollout credits once. The old counter stored no dollars.
CREATE TABLE IF NOT EXISTS pooltune_app.free_plan_rollout (
    singleton boolean PRIMARY KEY CHECK (singleton)
);
WITH first_install AS (
    INSERT INTO pooltune_app.free_plan_rollout VALUES (true)
    ON CONFLICT DO NOTHING RETURNING singleton
)
INSERT INTO pooltune_app.free_model_usage(owner_id, month, credits, micros, settled)
SELECT p.owner_id, date_trunc('month', b.utc_day)::date, sum(b.calls)::integer, 0, true
FROM first_install, pooltune_app.model_budgets b JOIN pooltune_app.projects p ON p.id = b.project_id
GROUP BY p.owner_id, date_trunc('month', b.utc_day)::date;

CREATE OR REPLACE FUNCTION pooltune_app.free_plan_usage(account bigint)
RETURNS TABLE (credits bigint, account_micros bigint, pool_micros bigint)
LANGUAGE sql STABLE AS $$
    WITH amounts AS (
        SELECT u.*, owner_id = account AND month = date_trunc('month', statement_timestamp() AT TIME ZONE 'UTC')::date AS current_account
        FROM pooltune_app.free_model_usage u
    )
    SELECT coalesce(sum(credits) FILTER (WHERE current_account), 0)::bigint,
           coalesce(sum(micros) FILTER (WHERE current_account), 0)::bigint,
           coalesce(sum(micros), 0)::bigint FROM amounts
$$;

CREATE OR REPLACE FUNCTION pooltune_app.reserve_free_model(project text, expected_epoch bigint, maximum bigint)
RETURNS bigint LANGUAGE plpgsql VOLATILE AS $$
DECLARE account bigint; receipt bigint;
BEGIN
    IF maximum IS NULL OR maximum <= 0 OR maximum > 1000000 THEN RETURN NULL; END IF;
    -- Serialize admissions across accounts and processes before reading the shared pool.
    PERFORM pg_advisory_xact_lock(761994345011);
    SELECT owner_id INTO account FROM pooltune_app.projects
      WHERE id = project AND epoch = expected_epoch AND epoch > 0 AND NOT agent_paused AND NOT supabase_blocked FOR SHARE;
    IF NOT FOUND THEN RETURN NULL; END IF;
    INSERT INTO pooltune_app.free_model_usage(owner_id, month, credits, micros)
    SELECT account, date_trunc('month', statement_timestamp() AT TIME ZONE 'UTC')::date, 1, maximum
    FROM pooltune_app.free_plan_usage(account)
    WHERE credits < 50 AND account_micros + maximum <= 1000000 AND pool_micros + maximum <= 45000000
    RETURNING id INTO receipt;
    RETURN receipt;
END
$$;
