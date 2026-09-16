-- The activity relay no longer offers resumable cursors, so Forge keeps no
-- relay checkpoint or pending refresh work. Dropping these tables is one-way:
-- pending hints are covered by ordinary syncing.
DROP TABLE forge_relay_pending;
DROP TABLE forge_relay_cursors;
