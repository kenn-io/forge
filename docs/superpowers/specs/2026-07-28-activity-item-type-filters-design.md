# Activity Item-Type Filter Design

## Goal

Let maintainers independently show or hide pull-request and issue activity so
the Activity page can display repository-level events, such as default-branch
commits, without unrelated pull-request or issue threads.

## User Experience

Replace the mutually exclusive `All` / `PRs` / `Issues` selector with two
independent item-type toggles:

- `PRs`
- `Issues`

Both toggles are enabled by default, which is the existing `All` state. Either
toggle may be disabled independently, and disabling both is valid.

In Threaded view, disabling an item type hides every thread of that type and
all activity nested in those threads. In Flat view, it hides every row attached
to that item type. Repository-level activity is unaffected by the item-type
toggles, so disabling both leaves eligible default-branch commits and force
pushes visible.

The existing event-type controls remain orthogonal:

- Comments
- Reviews
- Commits
- Force pushes
- Notifications

For example, disabling both item types and every event type except Commits
produces a repository-level commits-only view. The reset action re-enables both
item types and every event type.

Desktop and mobile Activity surfaces use the same independent item-type model.
Controls must expose pressed or checked state accessibly and allow the empty
selection without silently restoring `All`.

## State And Filtering

Replace the three-value item filter (`all`, `prs`, `issues`) with a set of
enabled item types (`pr`, `issue`), defaulting to both.

Item-type visibility remains a presentation filter because activity types such
as comments, reviews, and commits can belong to either item type. Both Flat and
Threaded views filter every returned item against the enabled item-type set;
rows without a PR or issue item type remain eligible.

The activity request's `types` list continues to control event kinds. Its
`new_pr` and `new_issue` entries reflect the enabled item types so hidden
opening events are not fetched unnecessarily. Shared event kinds may still be
returned for hidden item types and are removed by the presentation filter.

## URL Behavior

The existing `types` query parameter remains the persisted representation:

- No `types` parameter means both item types and all event types are enabled.
- Presence of `new_pr` enables PR items.
- Presence of `new_issue` enables issue items.
- A filtered URL containing neither enables neither item type.

URL hydration reconstructs the independent item-type set directly from these
entries and then normalizes the request list using the existing event,
notification, and default-branch rules. No new query parameter or compatibility
translation layer is introduced.

## Components And Boundaries

- `packages/ui/src/stores/activity.svelte.ts` owns the enabled item-type set,
  request-type construction, and URL round-trip behavior.
- `packages/ui/src/components/ActivityFeed.svelte` renders the desktop toggles
  and applies item-type visibility to Flat and Threaded data.
- `packages/ui/src/views/MobileActivityView.svelte` renders equivalent mobile
  toggles and applies the same visibility rule.
- Existing activity API and database behavior remain unchanged.

Shared constants and helpers should express item-type defaults and filtering so
desktop and mobile do not reimplement selection semantics.

## Testing

Store tests cover:

- Default state with both item types enabled.
- Independent PR and issue selection in request construction.
- A valid empty item-type selection.
- URL round trips for both, either, and neither item type.
- Commits-only request construction with repository-level commits retained.

Component tests cover:

- Both toggles start enabled.
- Toggling PRs or Issues updates the request and hides the complete matching
  Flat rows or Threaded threads.
- Toggling both off leaves repository-level activity eligible.
- Reset restores both item types.

Browser coverage exercises the real Activity interaction and verifies that a
Threaded item-type toggle hides the complete thread rather than only its
`Opened` event. Existing affected frontend suites run after the final edit.

## Non-Goals

- No Activity API or database changes.
- No new server-backed preference.
- No change to event-type meanings.
- No redesign of thread grouping or repository-level activity rendering.
