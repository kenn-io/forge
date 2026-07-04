INSERT INTO middleman_item_workflow_state
    (repo_id, item_type, item_number, status, updated_at)
SELECT mr.repo_id, 'pr', mr.number, k.status, k.updated_at
FROM middleman_kanban_state k
JOIN middleman_merge_requests mr ON mr.id = k.merge_request_id
WHERE 1
ON CONFLICT(repo_id, item_type, item_number) DO UPDATE SET
    status     = excluded.status,
    updated_at = excluded.updated_at;

DROP TABLE middleman_kanban_state;
