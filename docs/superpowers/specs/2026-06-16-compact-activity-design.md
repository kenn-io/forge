# Compact Activity Design

## Summary

Add a persisted Normal/Compact activity layout for provider PR and issue detail views. Compact mode makes comments and reviews scan like the existing compact rows for commits, force-pushes, and lifecycle events: one event per row with type, author, single-line summary, and relative time.

The PR detail dropdown currently labeled `Filters` becomes `View`, matching the Activity page pattern. Issue detail gets the same `View` control for layout selection. Existing PR timeline filter preferences remain under the current `middleman-pr-timeline-filter` key.

## Goals

- Let users switch PR and issue detail activity between `Normal` and `Compact`.
- Persist the layout across PRs, issues, route changes, reloads, and embedded/detail drawer surfaces.
- Reuse `EventTimeline` ordering, thread grouping inputs, metadata parsing, and existing normal-mode interaction behavior.
- Preserve existing PR timeline filter localStorage state.
- Keep issue detail scope limited to layout selection, not new event-type filters.

## Non-Goals

- No backend or API changes.
- No migration or rename of `middleman-pr-timeline-filter`.
- No new compact timeline component.
- No compact toggle for Kata, Docs, Messages, or the global Activity feed.
- No setting-page default for this preference in this change.

## Current State

- `packages/ui/src/components/detail/EventTimeline.svelte` is shared by `PullDetail.svelte` and `IssueDetail.svelte`.
- `EventTimeline` already has an `isCompactEvent()` path for commits, force-pushes, cross-references, lifecycle/system events, and comment deletion.
- Comments, reviews, and review comments currently render as full cards with markdown bodies.
- `PRTimelineFilter.svelte` renders the PR detail filter dropdown and stores PR-only filtering in `prTimelineFilter.ts`.
- Issue detail currently renders `EventTimeline` without a timeline control.

## Design

### Shared Layout Preference

Create a small detail activity view preference module near the detail components, for example `detailActivityView.ts`.

- Type: `DetailActivityViewMode = "normal" | "compact"`
- Default: `normal`
- Storage key: `middleman-detail-activity-view`
- APIs:
  - `loadDetailActivityViewMode(): DetailActivityViewMode`
  - `saveDetailActivityViewMode(mode: DetailActivityViewMode): void`

The loader validates persisted values and falls back to `normal` on missing, invalid, corrupt, or unavailable storage. The saver catches storage errors.

### View Menu

Replace the PR timeline filter trigger label with `View` and include layout choices as the first dropdown section.

PR detail menu sections:

- `Layout`
  - `Normal`
  - `Compact`
- Existing PR content/visibility filter sections from `PRTimelineFilter`

Issue detail menu sections:

- `Layout`
  - `Normal`
  - `Compact`

The existing PR filter object and `middleman-pr-timeline-filter` key stay unchanged. Layout is a separate preference so issues do not need to understand PR-only filters.

The concrete component can either extend `PRTimelineFilter.svelte` into a detail activity view menu or introduce a small wrapper that composes the existing filter sections with a layout section. The implementation should avoid renaming persisted filter data.

### Timeline Rendering

Add a `displayMode?: DetailActivityViewMode` prop to `EventTimeline`, defaulting to `normal`.

In compact mode:

- All top-level timeline entries render through the compact row path.
- Thread replies render as separate compact rows, not hidden behind a thread toggle.
- Comment/review bodies use a single-line plain-text preview derived from the rendered event body source.
- Markdown is not rendered in compact rows.
- Compact rows are scan-first summaries. Inline edit, copy, reply, resolve, and review-thread snippet controls are not shown in compact rows.
- Users switch back to `Normal` mode when they need full comment bodies or inline timeline actions.
- Existing compact event summaries for commits, force-pushes, cross-references, and lifecycle/system events remain intact.
- Existing ordering and force-push boundary logic remains unchanged.

In normal mode:

- Current behavior remains unchanged.
- Existing compact rows continue to be used for events currently classified by `isCompactEvent()`.
- Comments, reviews, review comments, thread controls, reply composer, edit controls, copy controls, and review-thread snippets behave as they do today.

### Detail Integration

`PullDetail.svelte` owns the shared layout state for PR detail instances:

- Initialize from `loadDetailActivityViewMode()`.
- Save on menu changes.
- Pass `displayMode` to `EventTimeline`.
- Keep existing PR event filtering unchanged.

`IssueDetail.svelte` mirrors that layout state:

- Initialize from the same loader.
- Save on menu changes.
- Pass `displayMode` to `EventTimeline`.
- Add a section title row so the issue `View` control sits beside `Activity`.

Because both detail views are reused in drawer and embedded surfaces, no separate drawer-specific state is needed.

Layout state is loaded independently by each mounted detail component. Changing one mounted detail surface saves the new preference and updates that surface immediately; other already-mounted detail surfaces do not need cross-component live synchronization. They pick up the saved preference on remount or route reload.

## Error Handling

- Invalid localStorage values fall back to `normal`.
- localStorage read/write errors are swallowed, leaving the current in-memory UI state usable.
- Compact row summary extraction should tolerate empty, malformed, or non-string bodies and render a stable fallback such as the event summary or event type label.

## Accessibility And UX

- The dropdown trigger is labeled `View`.
- Layout items close on select, like Activity page view choices.
- Compact rows remain readable at narrow widths with stable columns or responsive wrapping that preserves one event per row.
- Row content uses truncation for long body previews and commit messages.
- Normal mode remains the default so no existing detail view changes until the user opts in.
- Compact mode prioritizes overview density over inline mutation controls.

## Testing

- Unit tests for detail activity view preference loading, validation, persistence, and storage failures.
- `EventTimeline` tests:
  - Compact mode renders comments and reviews as one-line rows.
  - Thread replies appear as separate one-line rows.
  - Commit and lifecycle compact rows keep existing summaries.
  - Normal mode preserves current card rendering for comments/reviews.
- PR detail tests:
  - Trigger label is `View`.
  - Layout changes persist to `middleman-detail-activity-view`.
  - PR filter storage key remains `middleman-pr-timeline-filter`.
- Issue detail tests:
  - Activity section renders a `View` control.
  - Compact selection is loaded from and saved to the shared layout key.

Add affected e2e coverage for the user-visible toggle flow. The e2e should exercise the detail view with realistic provider data, switch to compact mode, verify one-line activity rows for comments/reviews, navigate to another PR or issue detail, and verify the compact preference persists.
