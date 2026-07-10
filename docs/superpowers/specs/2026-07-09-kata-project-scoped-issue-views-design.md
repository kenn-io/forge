# Kata Project-Scoped Issue Views

## Problem

Project-scoped Kata views resolve their visible rows through `KataTaskAPI.issues()`, but that method currently fetches the all-project issue list and filters it in the frontend. A separate project-scoped search request can make route-level coverage pass even while the generic list supplies the rows that are ultimately rendered.

## Design

When an issue-view query includes `project_uid`, the client will resolve the first available explicit, active, or stored-default daemon ID before the operation starts. If none is available, it will load the existing daemon roster on demand and use its marked default or first daemon. A known project will call `fetchIssuesByStatus(status, daemonId, project, true)` so open views use `/api/v1/projects/{id}/issues?status=open` and logbook views preserve `/api/v1/projects/{id}/issues?status=closed&limit=500`. An unknown project UID will produce an empty view without falling back to `/api/v1/issues`.

Queries without `project_uid` will retain the generic all-project list. The existing local scope predicate will still apply area filtering after project-scoped rows load and serves as defense-in-depth for project identity.

Public search operations will use the same resolver and pin the concrete result across project catalog, project list, text search, and label-hydration requests so numeric project IDs never cross daemon boundaries. This on-demand fallback gives callers outside Kata workspace the same readiness contract as roster-ready workspace operations.

## Error Handling

Project catalog and issue-list transport errors continue through the existing `KataTaskAPIError` path. If the roster cannot provide any concrete daemon, the operation fails before project reads with status `503` and code `service_unavailable`. If a concretely pinned daemon disappears during an operation, its server error propagates without falling back to another daemon. An unknown project UID is treated as an empty scope because it can represent a stale bookmarked route or a removed project.

## Verification

Task-client unit tests will assert operation-wide pinning, on-demand roster resolution, stable failure without a concrete daemon, and empty views for unknown project UIDs. A two-daemon full-stack test will stall the starting daemon's catalog response, change the configured default, then prove the resumed project list still reaches the starting backend and no foreign row renders. The full Kata Playwright browser matrix and full frontend Vitest suite will run before commit.
