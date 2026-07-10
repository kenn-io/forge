# Kata Project-Scoped Issue Views

## Problem

Project-scoped Kata views resolve their visible rows through `KataTaskAPI.issues()`, but that method currently fetches the all-project issue list and filters it in the frontend. A separate project-scoped search request can make route-level coverage pass even while the generic list supplies the rows that are ultimately rendered.

## Design

When an issue-view query includes `project_uid`, the client will resolve the first available explicit, active, or stored-default daemon ID before the operation starts. If none is available, it will load the existing daemon roster on demand and use its marked default or first daemon. A known project will call `fetchIssuesByStatus(status, daemonId, project, true)` so open views use `/api/v1/projects/{id}/issues?status=open` and logbook views preserve `/api/v1/projects/{id}/issues?status=closed&limit=500`. An unknown project UID will produce an empty view without falling back to `/api/v1/issues`.

Queries without `project_uid` will retain the generic all-project list, but every `issues()` operation will still resolve a concrete daemon before its first issue request. The existing local scope predicate will still apply area filtering after project-scoped rows load and serves as defense-in-depth for project identity.

Public search operations will use the same resolver and pin the concrete result across project catalog, project list, text search, and label-hydration requests so numeric project IDs never cross daemon boundaries. This on-demand fallback gives callers outside Kata workspace the same readiness contract as roster-ready workspace operations. A successful fallback is cached by the client; simultaneous cold-start operations may make duplicate read-only roster requests, which is acceptable because Kata workspace normally loads the roster before task operations.

Issue-view and search responses carry their concrete `daemon_id`. The workspace store binds that daemon in the task client only after its request-generation guard accepts the response; stale, failed, and unrelated palette/Docs/Messages search results cannot replace the workspace binding. Other daemon-backed reads and mutations use the bound workflow daemon before active selection, stored roster default, and on-demand fallback. A new list/search operation still resolves the current active/default selection without consulting the old binding, so an intentional switch can load another daemon while old displayed rows remain safely bound until the replacement succeeds. If a bound daemon is removed or becomes unhealthy, requests fail against that daemon without falling through to another backend. Multi-request issue creation prefers the current workflow daemon and pins its optional metadata follow-up to the same concrete daemon.

Each `events()` call resolves the effective workflow daemon once and pins every page in its event-log walk to that ID, so a selector change cannot combine event histories from different daemons. The workspace live-event stream uses the store's accepted daemon and its corresponding cursor until a replacement view/search succeeds.

## Error Handling

Project catalog and issue-list transport errors continue through the existing `KataTaskAPIError` path. Roster transport failures, malformed roster responses, empty rosters, and rosters without a truthy default-or-first ID are all treated as having no concrete daemon; the operation fails before project reads with status `503` and code `service_unavailable`. Health is not part of selection: if a configured daemon is disconnected or disappears after selection, its request error propagates without falling back to another daemon. An unknown project UID is treated as an empty scope because it can represent a stale bookmarked route or a removed project.

## Verification

Task-client and workspace-store tests will assert issue/search/create/event operation pinning, accepted-result ownership, stale-result rejection, on-demand roster resolution, stable failure without a concrete daemon, workflow-bound detail/mutation reads, and empty views for unknown project UIDs. A two-daemon full-stack test will stall the starting daemon's scoped catalog response, refresh the browser roster to a different default, then prove the resumed project list, live stream, selected row detail, and completion mutation still reach the starting backend despite colliding task IDs. The full Kata Playwright browser matrix and full frontend Vitest suite will run before commit.
