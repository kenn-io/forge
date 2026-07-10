# Kata Project-Scoped Issue Views

## Problem

Project-scoped Kata views resolve their visible rows through `KataTaskAPI.issues()`, but that method currently fetches the all-project issue list and filters it in the frontend. A separate project-scoped search request can make route-level coverage pass even while the generic list supplies the rows that are ultimately rendered.

## Design

When an issue-view query includes `project_uid`, the client will snapshot the active daemon ID, explicitly pin even the default daemon with an empty selection header, fetch that daemon's project catalog, and resolve the UID to its numeric Kata project ID. A known project will call `fetchIssuesByStatus(status, daemonId, project, true)` so open views use `/api/v1/projects/{id}/issues?status=open` and logbook views preserve `/api/v1/projects/{id}/issues?status=closed&limit=500`. An unknown UID will produce an empty view without falling back to `/api/v1/issues`.

Queries without `project_uid` will retain the generic all-project list. The existing local scope predicate will still apply area filtering after project-scoped rows load and serves as defense-in-depth for project identity.

Public search operations will snapshot and pin the same daemon across project catalog, project list, text search, and label-hydration requests so numeric project IDs never cross daemon boundaries.

## Error Handling

Project catalog and issue-list transport errors continue through the existing `KataTaskAPIError` path. An unknown project UID is treated as an empty scope rather than a transport error because it can represent a stale bookmarked route or a project removed since navigation state was persisted.

## Verification

Task-client unit tests will assert that known projects use only project issue-list routes, catalog/list/search requests keep the starting explicit or default daemon, and unknown UIDs return no rows without requesting the generic list. The full-stack Kata backend will serve project issue lists and allow `/api/v1/issues` to expose a contaminating row that must not appear in the final scoped view. The full Kata Playwright browser matrix and full frontend Vitest suite will run before commit.
