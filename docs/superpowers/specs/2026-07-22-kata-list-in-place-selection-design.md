# Kata List In-Place Selection Design

## Problem

Selecting a visible Kata task accepts a fresh workspace snapshot. The workspace currently treats the snapshot's `fetched_at` value as part of the issue-list structure. Because that timestamp changes on every accepted selection, the workspace increments `listResetGeneration`, remounts `KataIssueList`, and lets the browser reconstruct focus and scroll position. The result is a visible vertical jump even when the task rows have not structurally changed.

## Behavior

- Ordinary pointer or keyboard selection updates the existing task list in place.
- The list preserves its scroll position and focused row while selected detail, comments, events, and selection styling update.
- Background refreshes insert, remove, or update rows in place. If the next selected row is already visible, its viewport coordinate remains stable even when that accepted selection snapshot changes rows above it.
- A newly accepted snapshot may clear stale tree expansion when the visible task structure changes, but that reset does not remount the list or reset its scroll container.
- `fetched_at` is presentation freshness, not structural identity, and cannot trigger a list remount.
- Initial routed selections and hidden nested selections retain the existing one-time reveal behavior, including ancestor expansion and `scrollIntoView({ block: "nearest" })`.
- A daemon switch remains an authority-identity boundary and may create a fresh list instance.

## Implementation

Remove `acceptedCurrentView.fetched_at` from `currentExpansionSignature()` in `KataWorkspace.svelte`. Keep the remaining authority and issue fields so genuine structural changes can still clear stale expansion. Remove `listResetGeneration` from the list's keyed identity so those clears happen through the existing `resetGeneration` prop without remounting; retain the daemon ID as the key.

In `KataIssueList`, use a pre-update DOM measurement to capture the next selected row's offset when snapshot-backed list props change. After Svelte updates the keyed rows, adjust the existing scroll container by the row's offset delta. A selection change remains anchorable when its target row already exists onscreen; hidden routed selections skip naturally because no pre-update row is measurable. Also skip anchoring when the target was offscreen, disappeared, or the list instance changed.

Add a workspace integration regression that selects a visible row through the real accepted-snapshot path and proves the original list element and `scrollTop` survive. Add browser regressions that disable native browser scroll anchoring and prove both an existing selection and a newly selected visible row retain their viewport coordinate when a row is inserted above. Add full-stack Playwright coverage through the real Middleman HTTP/SQLite fixture for the combined selected-snapshot insertion and a production compact reset: unrelated expansion must collapse while the focused selected child's ancestor chain, visibility, selection, viewport position, and focus are restored. Keep the existing reveal tests unchanged to protect deep-link behavior.

## Verification

- The workspace remount regression and selected-row anchoring browser regressions fail before the production changes and pass afterward, including selection change plus insertion above the next selected row.
- Focused Playwright scenarios prove visible selected-snapshot anchoring and compact-reset recovery through a real daemon-target rotation and the Middleman server fixture.
- Existing Kata list and workspace tests pass.
- Svelte autofixer reports no new issues.
- Manual Computer Use selection in both local and federated daemons preserves the user's scan position.
