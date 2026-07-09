# Kata Project-Scoped Issue Views

## Problem

Project-scoped Kata views resolve their visible rows through `KataTaskAPI.issues()`, but that method currently fetches the all-project issue list and filters it in the frontend. A separate project-scoped search request can make route-level coverage pass even while the generic list supplies the rows that are ultimately rendered.

## Design

When an issue-view query includes `project_uid`, the client will fetch the project catalog first and resolve the UID to its numeric Kata project ID. A known project will load the requested open or closed rows through `/api/v1/projects/{id}/issues`, using the existing `fetchIssuesByStatus` project argument. An unknown UID will produce an empty view without falling back to `/api/v1/issues`.

Queries without `project_uid` will retain the generic all-project list. The existing local scope predicate remains responsible for view-specific filters such as area and serves as defense-in-depth for project identity.

## Error Handling

Project catalog and issue-list transport errors continue through the existing `KataTaskAPIError` path. An unknown project UID is treated as an empty scope rather than a transport error because it can represent a stale bookmarked route or a project removed since navigation state was persisted.

## Verification

A task-client unit test will assert that a known project uses only the project issue-list route and that an unknown UID returns no rows without requesting the generic list. The existing full-stack scoped-route test will be strengthened so `/api/v1/issues` exposes a contaminating row that must not appear; the project list will provide the only valid scoped row. The relevant client test, affected Kata Playwright test, and full frontend Vitest suite will run before commit.
