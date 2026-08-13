-- Discovery inventory can complete before an operator promotes a repository to
-- full archive mode. Maintenance must resume from the discovery-state creation
-- boundary, not the later promotion time, or items updated between those events
-- never enter archive work.
UPDATE forge_archive_repos
SET initial_started_at = created_at,
    maintenance_watermark = created_at,
    maintenance_succeeded_at = NULL,
    prompt_scan_started_at = NULL,
    prompt_since = NULL
WHERE collection_mode = 'full'
  AND initial_started_at IS NOT NULL
  -- Forge writes UTC time.Time values in Go's SQLite text representation.
  -- Comparing those canonical values directly preserves subsecond precision;
  -- SQLite's date functions return NULL for the trailing " +0000 UTC" form.
  AND initial_started_at > created_at;
