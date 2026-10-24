CREATE TABLE IF NOT EXISTS schema_version (id INTEGER PRIMARY KEY CHECK (id = 1), version INTEGER NOT NULL);
INSERT OR IGNORE INTO schema_version(id,version) VALUES (1,1);

CREATE TABLE IF NOT EXISTS events (
    seq INTEGER PRIMARY KEY AUTOINCREMENT,
    recorded_at TEXT NOT NULL,
    id TEXT NOT NULL UNIQUE,
    idempotency_key TEXT NOT NULL UNIQUE,
    target TEXT NOT NULL,
    actor TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('intent', 'uncertain', 'confirmed')),
    revision TEXT NOT NULL,
    predecessor TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS events_target_seq ON events(target, seq);
CREATE TRIGGER IF NOT EXISTS events_no_update BEFORE UPDATE ON events
BEGIN SELECT RAISE(ABORT, 'event history is append-only'); END;
CREATE TRIGGER IF NOT EXISTS events_no_delete BEFORE DELETE ON events
BEGIN SELECT RAISE(ABORT, 'event history is append-only'); END;

CREATE TABLE IF NOT EXISTS targets (
    target TEXT PRIMARY KEY,
    revision TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS revisions (
    target TEXT NOT NULL,
    id TEXT NOT NULL,
    predecessor TEXT NOT NULL,
    actor TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('pending', 'uncertain', 'active', 'superseded')),
    PRIMARY KEY (target, id),
    FOREIGN KEY (target) REFERENCES targets(target)
);
CREATE TABLE IF NOT EXISTS operations (
    target TEXT NOT NULL,
    revision TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('pending', 'uncertain', 'confirmed')),
    PRIMARY KEY (target, revision),
    FOREIGN KEY (target, revision) REFERENCES revisions(target, id)
);

-- Reserved projection tables. Owning tasks extend them with typed fields.
CREATE TABLE IF NOT EXISTS policies (target TEXT NOT NULL, revision TEXT NOT NULL, PRIMARY KEY(target, revision), FOREIGN KEY(target, revision) REFERENCES revisions(target, id));
CREATE TABLE IF NOT EXISTS guardrails (target TEXT NOT NULL, revision TEXT NOT NULL, PRIMARY KEY(target, revision), FOREIGN KEY(target, revision) REFERENCES revisions(target, id));
CREATE TABLE IF NOT EXISTS experiments (target TEXT NOT NULL, revision TEXT NOT NULL, PRIMARY KEY(target, revision), FOREIGN KEY(target, revision) REFERENCES revisions(target, id));
CREATE TABLE IF NOT EXISTS agent_runs (target TEXT NOT NULL, revision TEXT NOT NULL, PRIMARY KEY(target, revision), FOREIGN KEY(target, revision) REFERENCES revisions(target, id));
CREATE TABLE IF NOT EXISTS setup (target TEXT NOT NULL, revision TEXT NOT NULL, PRIMARY KEY(target, revision), FOREIGN KEY(target, revision) REFERENCES revisions(target, id));
