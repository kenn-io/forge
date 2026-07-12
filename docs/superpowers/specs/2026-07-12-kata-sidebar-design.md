# Kata Project UX Cleanup Design

## Goal

Remove two misleading Kata project interactions: easy project renaming in the navigation sidebar and issue reassignment hidden behind a clickable project breadcrumb. Kata navigation will also adopt the grouped-sidebar behavior introduced by PR #662.

## Sidebar Structure

`KataSidebar` will render one `SidebarScrollArea` in this order:

1. The existing system views, in their current order.
2. Kata areas, in the order supplied by the workspace store.
3. The existing new-project control.

Each area will use `GroupedSidebarSection`. Its header label is the area name and its count is the number of projects in that area. Areas start expanded. Collapse state lives for the mounted sidebar: it survives project selection and reactive data updates, but resets after the sidebar is remounted or the page reloads.

Expanded areas render projects in their existing order. Project selection, active-row state, open-task counts, status and error presentation, and project creation remain unchanged. Project rows use the shared sidebar styling contract, and narrow-viewport and overlay scroll-indicator behavior remain consistent with the other grouped rails.

## Sidebar Project Renaming

Only `KataSidebar` project-renaming controls are removed. Project rows will no longer contain a pencil button, respond to double-click by entering rename mode, or render an inline rename form. Rows remain native buttons, so pointer and keyboard activation continue to select the project without exposing a second interaction.

`KataWorkspace` will stop forwarding a rename callback to the sidebar. The Kata client and workspace-store rename operations remain available; this change does not remove daemon capability or define a replacement rename surface.

## Issue Project Reassignment

The current project in `KataIssueDetail` will become passive breadcrumb text. It will no longer look like navigation or open a picker when clicked.

The existing issue overflow menu will gain a **Move to another project** action. Selecting it opens a searchable destination picker within the overflow interaction. Eligible destinations preserve the current rules: exclude the current project and inbox-role projects, sort by project name, and show open-task counts. The action is absent when no destination is eligible.

Successful selection uses the existing `onMoveIssue` callback and closes the picker. Existing mutation errors continue through the workspace/store error path; the picker stays available when reassignment fails so the user can retry or dismiss it. Opening the menu, its actions, and the destination picker must remain keyboard reachable, with Escape returning focus out of the picker without moving the issue.

## State and Errors

The sidebar owns only project-creation and area-collapse state. Removing inline rename also removes rename draft, focus, saving, cancellation, and error state from the sidebar.

The issue overflow menu owns whether its normal actions or destination picker are visible and resets that state when the selected issue changes. Existing project-loading, project-creation, daemon, and issue-mutation error behavior otherwise remains unchanged.

## Acceptance Criteria

- Kata areas use the shared grouped section and scroll-area behavior without changing system-view or project ordering.
- Project navigation, active selection, open counts, project creation, and existing status/error presentation still work.
- No sidebar control, double-click, or keyboard path starts project renaming.
- The rename client and store operations remain available.
- The issue breadcrumb displays the current project name or existing UID fallback as passive text.
- Issue reassignment is available only through **More actions → Move to another project** and excludes ineligible destinations.
- The overflow interaction remains usable with pointer and keyboard input at desktop and narrow widths.

## Testing

Component tests will cover area ordering, default expansion, collapse and expansion, collapse-state lifetime, project navigation, project creation, and the absence of sidebar rename controls and inline rename behavior.

Issue-detail and overflow-menu component tests will prove that the project breadcrumb is passive, its fallback text remains correct, the move action is hidden without destinations, and selecting an eligible destination invokes reassignment. Store and client reassignment tests remain unchanged.

Full-stack Playwright coverage will replace the existing pencil-button and double-click rename scenarios. The replacement must prove project navigation and creation still work and that neither rename affordance is available. Existing browser coverage for grouped-sidebar scrolling remains the geometry-level check for the shared overlay indicator.

## Implementation Order

1. Migrate sidebar scrolling and area groups while preserving navigation and creation behavior.
2. Remove sidebar rename state, controls, callback wiring, and contradictory tests.
3. Make the issue breadcrumb passive and add overflow-menu reassignment.
4. Update component tests for both interactions.
5. Replace the obsolete full-stack rename scenarios and run the affected frontend suites.

## Scope

This change does not remove Kata's rename or move APIs, add a project-management page, modify daemon behavior, or change backend and database contracts.
