# Associated PR Workspace Linkage Design

## Goal

Make a pull-request detail expose the workspace already linked to that pull
request when the workspace began as issue-backed, Kata-backed, or ad-hoc work.
This lets the Activity detail claim and show the same workspace that the
Workspaces screen already identifies through `associated_pr_number`.

## Problem

Kenn Forge records a pull request discovered for an existing non-PR workspace
in `forge_workspaces.associated_pr_number`. Workspace summaries and runtime
features use that association, but the pull-detail lookup only matches a
workspace whose original `item_type` is `pull_request` and whose `item_number`
is the pull-request number. The pull-detail response therefore omits the
workspace reference for a later-associated workspace, even though the
Workspaces screen shows the PR link.

The pull-detail contract contains one optional `workspace` reference, while
more than one non-PR workspace can become associated with the same pull
request. Selection must therefore be explicit and deterministic.

## Selected Design

Add a provider-scoped database lookup dedicated to pull-detail linkage. The
workspace manager's `GetByMRForProvider` method will use this new lookup, while
the existing database `GetWorkspaceByMRForProvider` lookup remains direct-PR
only. In particular, merge-request sync continues to use the existing lookup
when reclassifying a direct PR workspace's head-repository trust; it must not
write that classification onto issue, Kata, or ad-hoc workspace rows.

Within the requested repository identity and pull-request number, the new
pull-detail lookup selects:

1. The directly PR-backed workspace, when one exists.
2. Otherwise, the newest non-PR workspace whose `associated_pr_number`
   matches, ordered by creation time and then workspace ID descending.

Direct PR ownership has precedence regardless of creation time. Associated
fallback candidates are limited to the item types that support later PR
association: issue, Kata task, and ad-hoc work. Workspace ID order is an
arbitrary but stable tie-breaker; it does not imply creation order.

Status does not affect selection. A newer associated workspace in `creating`
or `error` state wins over an older `ready` workspace, matching the direct
lookup's behavior and allowing the existing `{ id, status }` response to
explain the selected workspace's current state.

The lookup retains the current provider normalization, case-folded repository
route matching, and historical-route collision check. It does not infer an
association from branch names during a pull-detail read; the persisted
`associated_pr_number` remains authoritative.

## Data Flow

The dedicated database lookup returns the selected workspace through the
existing workspace manager method. Pull detail continues to convert that
workspace to the existing lightweight `{ id, status }` reference. No response
schema, generated client, or Svelte component changes are required.

Activity already loads the canonical pull-detail endpoint and claims a
workspace when that response contains the reference. Once the backend lookup
recognizes persisted associated workspaces, the Activity workspace pane and
terminal handoff follow the existing path.

Database failures keep the current best-effort detail behavior: pull detail
still renders if workspace resolution fails, while a missing eligible
workspace leaves the optional field absent. Historical route ambiguity also
continues to fail closed and returns no workspace.

## Testing

- Add a database regression proving the pull-detail-specific lookup returns a
  later-associated workspace for its pull request.
- Prove a direct PR workspace wins over newer associated candidates.
- Prove the newest associated workspace wins when no direct workspace exists,
  including the deterministic tie-breaker and status-independent selection.
- Keep the existing direct-only database lookup returning no workspace when
  only an associated workspace exists.
- Add a merge-request sync regression proving head-repository trust
  reclassification does not update an associated-only workspace.
- Add an API regression proving pull detail emits the selected workspace
  reference for a later-associated workspace.
- Retain the existing Activity component coverage that proves a workspace
  reference in pull detail is claimed and made available to the workspace
  pane.

The implementation will follow a red-green cycle: the focused lookup and API
tests must fail for the missing association before the query changes.

## Non-goals

- Returning multiple workspace references from pull detail.
- Changing how branch observation discovers or persists a PR association.
- Reassigning a workspace's original item type or item identity.
- Adding route-compatibility aliases or fallback inference.
- Changing repository route-reuse safety or workspace lifecycle behavior.
- Changing merge-request sync or head-repository trust classification.
