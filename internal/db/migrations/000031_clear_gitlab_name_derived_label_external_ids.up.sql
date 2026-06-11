-- GitLab MR/issue label normalization used to claim the label name as
-- platform_external_id. The label catalog now stores decimal GitLab
-- label IDs in the same field, so a legacy name-derived value can
-- collide with a different label whose decimal ID equals that name and
-- rewire the assignment to the wrong catalog label on the first
-- catalog refresh. Clear the name-derived external IDs; the rows
-- re-key by name on the next label write or catalog sync.
UPDATE middleman_labels
SET platform_external_id = ''
WHERE platform_external_id <> ''
  AND platform_external_id = name
  AND repo_id IN (SELECT id FROM middleman_repos WHERE platform = 'gitlab');
