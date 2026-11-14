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

-- Typed provider lifecycle is primary append-only history, not a projection.
CREATE TABLE IF NOT EXISTS provider_events(seq INTEGER PRIMARY KEY AUTOINCREMENT,target TEXT NOT NULL,revision TEXT NOT NULL,state TEXT NOT NULL,payload TEXT NOT NULL,recorded_at TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS provider_events_target ON provider_events(target,seq);
CREATE TRIGGER IF NOT EXISTS provider_events_no_update BEFORE UPDATE ON provider_events BEGIN SELECT RAISE(ABORT,'provider history is append-only'); END;
CREATE TRIGGER IF NOT EXISTS provider_events_no_delete BEFORE DELETE ON provider_events BEGIN SELECT RAISE(ABORT,'provider history is append-only'); END;

CREATE TABLE IF NOT EXISTS rollout_events (
 target TEXT NOT NULL,
 sequence INTEGER NOT NULL,
 event_id TEXT NOT NULL,
 payload TEXT NOT NULL,
 recorded_at INTEGER NOT NULL,
 PRIMARY KEY(target,sequence),
 UNIQUE(target,event_id)
);
CREATE TRIGGER IF NOT EXISTS rollout_no_update BEFORE UPDATE ON rollout_events BEGIN SELECT RAISE(ABORT,'append-only'); END;
CREATE TRIGGER IF NOT EXISTS rollout_no_delete BEFORE DELETE ON rollout_events BEGIN SELECT RAISE(ABORT,'append-only'); END;

CREATE TABLE IF NOT EXISTS admin_owner(id INTEGER PRIMARY KEY CHECK(id=1),claim_hash BLOB NOT NULL,claim_expires INTEGER NOT NULL,password_hash BLOB,claimed_at INTEGER);
CREATE TABLE IF NOT EXISTS admin_sessions(token_hash BLOB PRIMARY KEY,csrf TEXT NOT NULL,expires INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS admin_audit(seq INTEGER PRIMARY KEY AUTOINCREMENT,kind TEXT NOT NULL,recorded_at INTEGER NOT NULL);
CREATE TRIGGER IF NOT EXISTS admin_audit_no_update BEFORE UPDATE ON admin_audit BEGIN SELECT RAISE(ABORT,'append-only'); END;
CREATE TRIGGER IF NOT EXISTS admin_audit_no_delete BEFORE DELETE ON admin_audit BEGIN SELECT RAISE(ABORT,'append-only'); END;

-- Definitive rejection is primary audit evidence, not an applied revision.
-- Keep it separate from the existing immutable event format and projections.
CREATE TABLE IF NOT EXISTS activation_rejections (
 target TEXT NOT NULL, revision TEXT NOT NULL, checksum TEXT NOT NULL,
 epoch TEXT NOT NULL, recorded_at TEXT NOT NULL, PRIMARY KEY(target,revision)
);
CREATE TRIGGER IF NOT EXISTS activation_rejections_no_update BEFORE UPDATE ON activation_rejections
BEGIN SELECT RAISE(ABORT,'rejection history is append-only'); END;
CREATE TRIGGER IF NOT EXISTS activation_rejections_no_delete BEFORE DELETE ON activation_rejections
BEGIN SELECT RAISE(ABORT,'rejection history is append-only'); END;
