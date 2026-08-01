# Activity Filter Session Persistence Design

## Problem

Activity feed filters live in the Activity URL. While the user visits another
mode, the router remembers that URL only in module memory. A page reload or a
browser tab discard while Workspaces is active recreates the router with no
Activity URL to restore, so returning to Activity uses the default filters and
can re-enable item and event types the user disabled.

## Decision

Persist the router's last validated Activity route in `sessionStorage`. The URL
remains the source of truth for Activity state; storage only preserves that URL
while another mode is active. Session scope keeps the filters through reloads
and tab restoration without making them a permanent cross-session preference or
overriding configured Activity defaults in a new browser session.

On router initialization, restore the cached value only when it parses as an
Activity route. Continue to use `/` when storage is unavailable, empty, or
invalid. Every existing update to the in-memory Activity route cache also
updates the session value. Storage access must tolerate browsers or embedded
hosts that block web storage.

Alternatives rejected:

- Persisting individual filters in local storage would create a second source
  of truth and could shadow configured defaults across browser sessions.
- Keeping Activity mounted while hidden would not survive a reload and would
  add lifecycle and polling complexity without fixing the reported trigger.

## Testing

Add router coverage that records a filtered Activity URL, navigates to
Workspaces, reloads the router module while still on Workspaces, and verifies
that the previous Activity route is restored. Also cover invalid cached routes
and blocked storage so they safely fall back to `/`.

The existing Activity component and browser tests continue to cover filter URL
encoding and presentation. No API, backend, or visual styling changes are
required.
