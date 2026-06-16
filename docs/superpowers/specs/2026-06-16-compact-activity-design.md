# Compact Activity Design

## Summary

Add a persisted Normal/Compact activity layout for provider PR and issue detail views. Compact mode renders activity as an aligned row grid: one event per row with type, author, context, single-line summary, and relative time.

The PR detail dropdown currently labeled `Filters` becomes `View`, matching the Activity page pattern. Issue detail gets the same `View` control for layout selection. Existing PR timeline filter preferences remain under the current `middleman-pr-timeline-filter` key.

## Goals

- Let users switch PR and issue detail activity between `Normal` and `Compact`.
- Persist the layout across PRs, issues, route changes, reloads, and embedded/detail drawer surfaces, with live updates across mounted detail surfaces.
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

Create a small localStorage-backed rune store following the `grouping.svelte.ts` pattern at `packages/ui/src/stores/detail-activity-view.svelte.ts`.

- Type: `DetailActivityViewMode = "normal" | "compact"`
- Default: `normal`
- Storage key: `middleman-detail-activity-view`
- APIs:
  - `createDetailActivityViewStore()`
  - `getMode(): DetailActivityViewMode`
  - `setMode(mode: DetailActivityViewMode): void`

The store validates persisted values and falls back to `normal` on missing, invalid, corrupt, or unavailable storage. `setMode()` updates reactive in-memory state first and catches storage write errors. `Provider.svelte` should create and expose this store with the other UI stores so PR and issue details share one live preference instance.

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

Implement a neutral `DetailActivityViewMenu.svelte` component using the existing shared `FilterDropdown` primitive. The PR detail instance includes both the shared `Layout` section and existing PR filter sections. The issue detail instance includes only the shared `Layout` section. Avoid using a PR-named component for the issue menu, and avoid renaming persisted filter data.

### Timeline Rendering

Add a `displayMode?: DetailActivityViewMode` prop to `EventTimeline`, defaulting to `normal`.

In compact mode:

- All timeline entries render as aligned compact rows using one shared grid layout for type, author, context, summary, and time.
- The existing compact event rendering path provides source logic and event-specific summaries, but the compact visual layout should become column-aligned for every compact row so commit/lifecycle rows and comment/review rows scan uniformly.
- Thread replies render as separate compact rows, not hidden behind a thread toggle.
- Comment bodies use a single-line plain-text preview from the raw markdown body. Use the first meaningful text after removing obvious markdown/link/image/code-fence markers and collapsing whitespace. Do not render markdown or strip rendered HTML in compact rows.
- Review rows surface the review verdict when present, for example `Approved`, `Changes requested`, or `Commented`, followed by any body preview.
- Review-comment rows include file/line context when available, for example `src/file.ts:42`, before the body preview.
- Markdown is not rendered in compact rows.
- Compact rows are scan-first summaries. Inline edit, copy, reply, resolve, and review-thread snippet controls are not shown in compact rows.
- There is no per-row expand, jump, or click-through behavior in v1. Users switch back to `Normal` mode when they need full comment bodies or inline timeline actions.
- Existing compact event summaries for commits, force-pushes, cross-references, and lifecycle/system events remain intact.
- Existing ordering and force-push boundary logic remains unchanged.

Preview examples:

- `[docs](https://example.com) updated` -> `docs updated`
- `![screenshot](image.png) Looks good` -> `Looks good`
- A fenced code block followed by prose -> the first prose line after the fence, if present; otherwise the first code line collapsed to text.

In normal mode:

- Current behavior remains unchanged.
- Existing compact rows continue to be used for events currently classified by `isCompactEvent()`.
- Comments, reviews, review comments, thread controls, reply composer, edit controls, copy controls, and review-thread snippets behave as they do today.

### Detail Integration

`PullDetail.svelte` uses the shared layout state for PR detail instances:

- Read and update the shared detail activity view store.
- Pass `displayMode` to `EventTimeline`.
- Keep existing PR event filtering unchanged.

`IssueDetail.svelte` uses the same layout state:

- Read and update the same shared detail activity view store.
- Pass `displayMode` to `EventTimeline`.
- Add a section title row so the issue `View` control sits beside `Activity`.

Because both detail views are reused in drawer and embedded surfaces, no separate drawer-specific state is needed.

Because the preference is a shared rune store, changing one mounted detail surface updates other mounted PR/issue detail surfaces that read the same store instance.

## Error Handling

- Invalid localStorage values fall back to `normal`.
- localStorage read/write errors are swallowed, leaving the current in-memory UI state usable.
- Compact row summary extraction should tolerate empty, malformed, or non-string bodies and render a stable fallback such as the event summary, review verdict, file/line context, or event type label.

## Accessibility And UX

- The dropdown trigger is labeled `View`.
- Layout items close on select, like Activity page view choices.
- Compact rows use stable columns on desktop/tablet widths. At narrow widths, the row may collapse context under the summary or wrap within the same event row, but it must preserve one event per row.
- Row content uses truncation for long body previews and commit messages.
- Normal mode remains the default so no existing detail view changes until the user opts in.
- Compact mode prioritizes overview density over inline mutation controls.

## Testing

- Vitest unit tests for the detail activity view store loading, validation, persistence, live reactive updates, and storage failures.
- `EventTimeline` tests:
  - Compact mode renders comments and reviews as one-line rows.
  - Thread replies appear as separate one-line rows.
  - Review compact rows surface verdicts.
  - Review-comment compact rows include file/line context when available.
  - Commit and lifecycle compact rows keep existing summaries.
  - Normal mode preserves current card rendering for comments/reviews.
- PR detail tests:
  - Trigger label is `View`.
  - Layout changes persist to `middleman-detail-activity-view`.
  - PR filter storage key remains `middleman-pr-timeline-filter`.
- Issue detail tests:
  - Activity section renders a `View` control.
  - Compact selection is loaded from and saved to the shared layout key.

Add affected Playwright e2e coverage for the user-visible toggle flow. The e2e should exercise the detail view with realistic provider data, switch to compact mode, verify aligned one-line activity rows for comments/reviews/review comments, navigate to another PR or issue detail, and verify the compact preference persists. Before pushing frontend implementation work, run the full affected Playwright e2e suite after the final frontend/test edit.
