-- Removes only the marker written for existing installations; markers from
-- reported setups stay.
DELETE FROM forge_app_metadata
WHERE key = 'telemetry.setup_finished' AND value = 'existing';
