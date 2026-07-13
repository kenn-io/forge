# ScrollBox: shared floating scroll indicator for major scroll panes

**Date:** 2026-07-13
**Status:** Approved

## Goal

The list sidebars use a floating, auto-hiding scroll indicator (`SidebarScrollArea`) instead of a native scrollbar. Extract that behavior into a general `ScrollBox` component and apply it to the other big, always-visible scroll panes: the diff views, the pull/issue detail (timeline) scrollers, the main list views, and the activity panes.

## Component

`packages/ui/src/components/shared/ScrollBox.svelte`, extracted from `SidebarScrollArea.svelte`. `SidebarScrollArea.svelte` is deleted and its consumers migrate to `ScrollBox`; the `SidebarScrollArea` export in `packages/ui/src/index.ts` is replaced by a `ScrollBox` export. The geometry helper `sidebarScrollIndicator.ts` is renamed to `scrollIndicator.ts` (same logic, same tests, renamed alongside).

Structure (unchanged from `SidebarScrollArea`): positioned root, scrolling viewport with the native scrollbar hidden (`scrollbar-width: none` + webkit equivalent), content wrapper, and an absolutely positioned indicator overlay. CSS classes are renamed `sidebar-scroll-*` to `scroll-box*` equivalents.

Props:

- `children` — snippet rendered inside the content wrapper.
- `label` — required accessible name for the viewport (`role="region"`, `tabindex="0"`).
- `class` — applied to the root (layout sizing: flex, min-height).
- `dataTest` — `data-test` on the root.
- `onscroll` — optional callback invoked from the internal scroll handler after indicator state updates.
- `viewport` — bindable export of the viewport element, so hosts can keep imperative scroll logic (scroll restore, programmatic scrolling, listener attachment via `$effect`).
- Rest props are spread onto the viewport div (e.g. `style:` equivalents pass as `style`, `overscroll-behavior`, extra aria attributes).

Indicator behavior is unchanged: thumb appears while scrolling and fades 700ms after the last scroll event, minimum thumb height 24px, hidden entirely when content fits, geometry from `getScrollIndicatorGeometry()`, `prefers-reduced-motion` disables the fade transition, `forced-colors` uses `CanvasText`.

`ScrollBox` is vertical-only. Horizontal thumb support is out of scope; two-axis scrollers keep native scrolling (see exclusions).

## Adoption scope

Converted in this change:

| Surface | File | Notes |
|---|---|---|
| Diff area | `packages/ui/src/components/diff/DiffView.svelte` (`.diff-area`) | Covers the PR Files tab, workspace diff panel, and commit diff panel. Uses `bind:viewport={diffArea}`; existing scroll restore, active-file tracking, paging, wheel containment, and virtualizer wiring stay on the bound element. Horizontal code scrolling is per-file inside the Pierre diff component and is unaffected. |
| Pull detail | `packages/ui/src/components/detail/PullDetail.svelte` (`.pull-detail`) | Uses `bind:viewport` for the existing scroll position save/restore. |
| Issue detail | `packages/ui/src/components/detail/IssueDetail.svelte` | Simple wrap; no scroll wiring today. |
| Focus list | `packages/ui/src/views/FocusListView.svelte` | Simple wrap. |
| Reviews list | `packages/ui/src/views/ReviewsView.svelte` | Simple wrap. |
| Mobile activity | `packages/ui/src/views/MobileActivityView.svelte` | Simple wrap. |
| Activity feed | `packages/ui/src/components/ActivityFeed.svelte` | Simple wrap. |
| Threaded activity | `packages/ui/src/components/ActivityThreaded.svelte` | Simple wrap. |
| List sidebars | `PullList.svelte`, `IssueList.svelte`, `frontend/.../WorkspaceListSidebar.svelte` | Mechanical rename from `SidebarScrollArea` to `ScrollBox`. |

When a host's scroll element carried layout or spacing CSS, sizing rules (flex, min-height, min-width) move to the `ScrollBox` root via `class`, and padding/spacing rules move onto the host's own content markup inside `children`; visual output must not change apart from the scrollbar treatment.

Explicitly not converted (native scrolling kept): popovers, pickers, dropdowns, modals, trays, the diff file tree and commit list, log/prompt viewers, kanban columns, tables and other two-axis scrollers (`RepoPreviewTable`, `LinkedMessagesView`, repo-browser code panes, `CIStatus`/`StackStatus`), `<textarea>`/contenteditable internals, and xterm terminals. Other surfaces may adopt `ScrollBox` later as they are touched.

## Documentation

Update the `SidebarScrollArea` reference in `context/ui-design-system.md` to name `ScrollBox` and its wider applicability (keep it a terse invariant, not a walkthrough).

## Testing

- The geometry helper tests move with the rename (`scrollIndicator.test.ts`), unchanged.
- New jsdom Vitest component tests for `ScrollBox`: thumb becomes visible on scroll and hides after the timeout, thumb hidden when content fits, `viewport` binding exposes the scrolling element, `onscroll` callback fires, accessible label present.
- Existing suites are the regression net for the converted hosts (DiffView, PullDetail, list views, sidebar tests). No test currently selects the `sidebar-scroll-*` classes.
- Manual/e2e risk areas to verify: diff scroll restore and file jumping, pull detail scroll save/restore, diff virtualizer behavior.
- Full `vp test` and the affected Playwright suites run before push since shared components change.
