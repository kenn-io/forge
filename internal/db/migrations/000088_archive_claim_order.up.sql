ALTER TABLE forge_archive_dataset_progress ADD COLUMN item_created_at DATETIME;

UPDATE forge_archive_dataset_progress AS p
SET item_created_at = (
    SELECT ai.provider_created_at FROM forge_archive_items ai
    WHERE ai.repo_id = p.repo_id AND ai.item_type = p.item_type AND ai.item_number = p.item_number
)
WHERE dataset = 'lookup';

CREATE INDEX idx_archive_dataset_progress_claim_order
    ON forge_archive_dataset_progress(repo_id, item_type, item_created_at, item_number)
    WHERE dataset = 'lookup' AND status IN ('pending', 'running', 'failed');
