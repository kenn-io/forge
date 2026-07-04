INSERT INTO middleman_item_workflow_state
    (repo_id, item_type, item_number, status, updated_at)
SELECT mr.repo_id, 'pr', mr.number, k.status, k.updated_at
FROM middleman_kanban_state k
JOIN middleman_merge_requests mr ON mr.id = k.merge_request_id
WHERE 1
ON CONFLICT(repo_id, item_type, item_number) DO UPDATE SET
    status     = excluded.status,
    updated_at = excluded.updated_at;

-- The workflow helpers reject statuses outside the canonical vocabulary, so
-- an invalid legacy status carried into the canonical table would be
-- unreadable and unfixable through the API. Normalize the whole table, not
-- just the re-synced rows, to also cover rows copied by 000037 whose kanban
-- counterpart has since been deleted.
UPDATE middleman_item_workflow_state
SET status = 'new'
WHERE status NOT IN ('new', 'reviewing', 'waiting', 'awaiting_merge');

DROP TABLE middleman_kanban_state;
