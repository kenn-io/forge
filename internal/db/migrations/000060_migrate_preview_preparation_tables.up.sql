-- Early migration-54 previews created node/coordinator names. Migration 56
-- repaired the triggers but left those tables behind. Normalize both shapes
-- here, preserving preparation state, receipts, seals, and acknowledgement
-- admissions in the migration transaction.
DROP TRIGGER forge_notification_ack_admission_update;
DROP TRIGGER forge_notification_ack_admission_insert;

CREATE TABLE IF NOT EXISTS forge_spoke_preparation (
    singleton_id INTEGER PRIMARY KEY CHECK (singleton_id = 1),
    phase TEXT NOT NULL DEFAULT 'open'
        CHECK (phase IN ('open', 'quiescing', 'sealed')),
    enrollment_id TEXT NOT NULL DEFAULT '',
    hub_node_id TEXT NOT NULL DEFAULT '',
    local_node_id TEXT NOT NULL DEFAULT '',
    protocol_version INTEGER NOT NULL DEFAULT 0,
    migration_version INTEGER NOT NULL DEFAULT 54
        CHECK (migration_version = 54),
    ack_generation INTEGER NOT NULL DEFAULT 0
        CHECK (ack_generation >= 0),
    drain_ack_generation INTEGER
        CHECK (drain_ack_generation IS NULL OR drain_ack_generation >= 0),
    preparation_digest TEXT NOT NULL DEFAULT '',
    preparation_seal TEXT NOT NULL DEFAULT '',
    started_at DATETIME,
    sealed_at DATETIME,
    updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS forge_spoke_preparation_receipts (
    state_kind TEXT NOT NULL
        CHECK (state_kind IN ('review_draft', 'workflow_state')),
    source_key TEXT NOT NULL,
    content_digest TEXT NOT NULL,
    hub_receipt TEXT NOT NULL,
    imported_at DATETIME NOT NULL,
    PRIMARY KEY (state_kind, source_key)
);

CREATE TABLE IF NOT EXISTS forge_spoke_preparation_seals (
    enrollment_id TEXT PRIMARY KEY,
    node_id TEXT NOT NULL,
    hub_node_id TEXT NOT NULL,
    protocol_version INTEGER NOT NULL,
    migration_version INTEGER NOT NULL CHECK (migration_version = 54),
    receipts_digest TEXT NOT NULL,
    drained_ack_generation INTEGER NOT NULL CHECK (drained_ack_generation >= 0),
    preparation_digest TEXT NOT NULL UNIQUE,
    preparation_seal TEXT NOT NULL UNIQUE,
    created_at DATETIME NOT NULL
);

-- SQLite has no conditional table rename. Empty legacy tables let the same
-- transfer run on already-current databases; existing preview tables remain
-- intact until their rows have been copied into the constrained current schema.
CREATE TABLE IF NOT EXISTS forge_node_preparation AS
SELECT singleton_id, phase, enrollment_id, hub_node_id AS coordinator_node_id,
       local_node_id, protocol_version, migration_version, ack_generation,
       drain_ack_generation, preparation_digest, preparation_seal,
       started_at, sealed_at, updated_at
FROM forge_spoke_preparation WHERE 0;

CREATE TABLE IF NOT EXISTS forge_node_preparation_receipts AS
SELECT state_kind, source_key, content_digest, hub_receipt AS coordinator_receipt,
       imported_at
FROM forge_spoke_preparation_receipts WHERE 0;

CREATE TABLE IF NOT EXISTS forge_node_preparation_seals AS
SELECT enrollment_id, node_id, hub_node_id AS coordinator_node_id,
       protocol_version, migration_version, receipts_digest,
       drained_ack_generation, preparation_digest, preparation_seal, created_at
FROM forge_spoke_preparation_seals WHERE 0;

INSERT INTO forge_spoke_preparation (
    singleton_id, phase, enrollment_id, hub_node_id, local_node_id,
    protocol_version, migration_version, ack_generation, drain_ack_generation,
    preparation_digest, preparation_seal, started_at, sealed_at, updated_at
)
SELECT singleton_id, phase, enrollment_id, coordinator_node_id, local_node_id,
       protocol_version, migration_version, ack_generation, drain_ack_generation,
       preparation_digest, preparation_seal, started_at, sealed_at, updated_at
FROM forge_node_preparation;

INSERT INTO forge_spoke_preparation_receipts (
    state_kind, source_key, content_digest, hub_receipt, imported_at
)
SELECT state_kind, source_key, content_digest, coordinator_receipt, imported_at
FROM forge_node_preparation_receipts;

INSERT INTO forge_spoke_preparation_seals (
    enrollment_id, node_id, hub_node_id, protocol_version, migration_version,
    receipts_digest, drained_ack_generation, preparation_digest,
    preparation_seal, created_at
)
SELECT enrollment_id, node_id, coordinator_node_id, protocol_version, migration_version,
       receipts_digest, drained_ack_generation, preparation_digest,
       preparation_seal, created_at
FROM forge_node_preparation_seals;

DROP TABLE forge_node_preparation_seals;
DROP TABLE forge_node_preparation_receipts;
DROP TABLE forge_node_preparation;

CREATE TRIGGER forge_notification_ack_admission_insert
AFTER INSERT ON forge_notification_items
WHEN NEW.source_ack_queued_at IS NOT NULL
 AND NEW.source_ack_synced_at IS NULL
BEGIN
    UPDATE forge_spoke_preparation
    SET ack_generation = ack_generation + 1,
        updated_at = datetime('now')
    WHERE singleton_id = 1;

    INSERT INTO forge_notification_ack_admissions (
        notification_id, generation, queued_at
    )
    SELECT NEW.id, ack_generation, NEW.source_ack_queued_at
    FROM forge_spoke_preparation
    WHERE singleton_id = 1
    ON CONFLICT(notification_id) DO UPDATE SET
        generation = excluded.generation,
        queued_at = excluded.queued_at;
END;

CREATE TRIGGER forge_notification_ack_admission_update
AFTER UPDATE OF source_ack_queued_at, source_ack_synced_at
ON forge_notification_items
WHEN NEW.source_ack_queued_at IS NOT NULL
 AND NEW.source_ack_synced_at IS NULL
 AND (
     OLD.source_ack_queued_at IS NULL
     OR OLD.source_ack_synced_at IS NOT NULL
     OR OLD.source_ack_queued_at <> NEW.source_ack_queued_at
 )
BEGIN
    UPDATE forge_spoke_preparation
    SET ack_generation = ack_generation + 1,
        updated_at = datetime('now')
    WHERE singleton_id = 1;

    INSERT INTO forge_notification_ack_admissions (
        notification_id, generation, queued_at
    )
    SELECT NEW.id, ack_generation, NEW.source_ack_queued_at
    FROM forge_spoke_preparation
    WHERE singleton_id = 1
    ON CONFLICT(notification_id) DO UPDATE SET
        generation = excluded.generation,
        queued_at = excluded.queued_at;
END;
