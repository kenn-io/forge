-- Installations that already hold work, or sealed spokes that read hub work,
-- were set up before setup telemetry existed. Mark them complete without
-- reporting so upgrades do not count as first-run setups.
INSERT OR IGNORE INTO forge_app_metadata (key, value, updated_at)
SELECT 'telemetry.setup_finished', 'existing', CURRENT_TIMESTAMP
WHERE EXISTS (SELECT 1 FROM forge_merge_requests)
   OR EXISTS (SELECT 1 FROM forge_issues)
   OR EXISTS (SELECT 1 FROM forge_workspaces WHERE status = 'ready')
   OR EXISTS (SELECT 1 FROM forge_spoke_preparation WHERE phase = 'sealed');
