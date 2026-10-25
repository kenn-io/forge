ALTER TABLE forge_mr_events ADD COLUMN commit_author TEXT NOT NULL DEFAULT '';

UPDATE forge_mr_events
SET commit_author = json_extract(metadata_json, '$.commit_author')
WHERE event_type = 'commit' AND json_valid(metadata_json)
  AND json_type(metadata_json, '$.commit_author') = 'text';
