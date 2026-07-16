ALTER TABLE middleman_archive_items
    ADD COLUMN hydration_snapshot_updated_at DATETIME;

UPDATE middleman_archive_items
SET hydration_snapshot_updated_at = provider_updated_at;
