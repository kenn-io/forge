-- A merge request's changed lines by kind of file, counted from the local
-- clone for the diff named by merge_base_sha and head_sha. Counts for an
-- older diff are not shown and are replaced once the new diff is counted.
CREATE TABLE forge_merge_request_line_kinds (
    merge_request_id INTEGER PRIMARY KEY REFERENCES forge_merge_requests(id) ON DELETE CASCADE,
    merge_base_sha TEXT NOT NULL,
    head_sha TEXT NOT NULL,
    code_additions INTEGER NOT NULL,
    code_deletions INTEGER NOT NULL,
    docs_additions INTEGER NOT NULL,
    docs_deletions INTEGER NOT NULL,
    data_additions INTEGER NOT NULL,
    data_deletions INTEGER NOT NULL,
    generated_additions INTEGER NOT NULL,
    generated_deletions INTEGER NOT NULL,
    counted_at DATETIME NOT NULL
);
