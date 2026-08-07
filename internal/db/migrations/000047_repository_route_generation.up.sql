ALTER TABLE forge_repo_routes
ADD COLUMN generation INTEGER NOT NULL DEFAULT 1;

-- Schema 45 could record a route's old and current owners without clearing
-- route-scoped state. Clean every currently published route with a historical
-- owner before normal reconciliation can take its same-owner fast path.
DELETE FROM forge_notification_items
WHERE repo_id IS NULL
  AND EXISTS (
      SELECT 1
      FROM forge_repo_routes AS current_route
      JOIN forge_repo_routes AS historical_route
        ON historical_route.platform = current_route.platform
       AND historical_route.platform_host = current_route.platform_host
       AND historical_route.repo_path_key = current_route.repo_path_key
       AND historical_route.repo_id <> current_route.repo_id
      WHERE current_route.is_current = 1
        AND historical_route.is_current = 0
        AND current_route.platform = forge_notification_items.platform
        AND current_route.platform_host = forge_notification_items.platform_host
        AND current_route.owner_key = forge_notification_items.repo_owner
        AND current_route.name_key = forge_notification_items.repo_name
  );

DELETE FROM forge_http_etags
WHERE EXISTS (
    SELECT 1
    FROM forge_repo_routes AS current_route
    JOIN forge_repo_routes AS historical_route
      ON historical_route.platform = current_route.platform
     AND historical_route.platform_host = current_route.platform_host
     AND historical_route.repo_path_key = current_route.repo_path_key
     AND historical_route.repo_id <> current_route.repo_id
    WHERE current_route.is_current = 1
      AND historical_route.is_current = 0
      AND current_route.platform = forge_http_etags.platform
      AND current_route.platform_host = forge_http_etags.platform_host
      AND current_route.owner_key = forge_http_etags.owner_key
      AND current_route.name_key = forge_http_etags.name_key
);

DELETE FROM forge_notification_sync_watermarks
WHERE EXISTS (
    SELECT 1
    FROM forge_repo_routes AS current_route
    JOIN forge_repo_routes AS historical_route
      ON historical_route.platform = current_route.platform
     AND historical_route.platform_host = current_route.platform_host
     AND historical_route.repo_path_key = current_route.repo_path_key
     AND historical_route.repo_id <> current_route.repo_id
    WHERE current_route.is_current = 1
      AND historical_route.is_current = 0
      AND current_route.platform = forge_notification_sync_watermarks.platform
      AND current_route.platform_host = forge_notification_sync_watermarks.platform_host
      AND current_route.owner_key = forge_notification_sync_watermarks.repo_owner
      AND current_route.name_key = forge_notification_sync_watermarks.repo_name
);
