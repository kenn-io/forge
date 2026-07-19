# Activity Detail Live Refresh Design

## Problem

When a sync persists a new pull-request event, the `data_changed` SSE signal
refreshes the Activity feed immediately. If that pull request is already open
in the Activity detail pane, its timeline is refreshed through a separate
sync-completion or targeted-detail path. The split invalidation paths let the
sidebar show the event several seconds before the detail timeline does.

The pull-detail GET endpoint reads the same persisted SQLite state and does not
contact the provider, so the detail can be refreshed as soon as
`data_changed` arrives.

## Design

On `data_changed`, keep the existing route-scoped refresh behavior. For the
Activity and mobile Activity routes, also inspect the currently displayed
detail. When it is a pull request, immediately call `refreshDetailOnly` with
the selected pull request's complete provider identity: provider, platform
host, repository path, owner, repository name, and number.

Do not refresh an issue detail through the pull-detail store, and do not add a
compatibility path or change the SSE payload. Existing detail-store generation
guards will discard a response if the user changes selection while the request
is in flight. Existing content comparison will preserve the displayed object
when a broad `data_changed` signal produces no actual detail change.

Targeted `pr_detail_refreshed` and `pr_ci_refreshed` handling remains intact.
Those events still cover provider-refresh completion and CI-specific changes;
the generic invalidation closes the earlier gap for already-persisted timeline
events.

## Testing

Extend the Provider event-wiring test with an Activity-route case that exposes
a selected pull request, invokes the captured `onDataChanged` callback, and
asserts that both `loadActivity` and `refreshDetailOnly` start immediately with
the full provider-aware identity.

Keep the existing route matrix to prove other routes retain their current
refresh scope. Run the focused Provider test, the full frontend test suite, the
frontend type check, and the Svelte autofixer for the edited Provider component.
