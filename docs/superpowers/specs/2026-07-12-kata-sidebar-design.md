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

The existing issue overflow menu will gain a **Move to another project** action. Selecting it replaces the normal action list with a dialog-like searchable destination panel. Eligible destinations preserve the current rules: exclude the current project and inbox-role projects, sort by project name, and show open-task counts. Duplicate names use the project area only when it uniquely identifies the destination within that duplicate-name group; otherwise they show the project UID. The action is absent when no destination is eligible.

`onMoveIssue` will return `boolean | Promise<boolean>` from both workspace hosts: `true` means the mutation succeeded and `false` means the workspace handled and displayed an error. Success closes the overflow interaction; failure leaves the picker and query available for retry. Each request receives a monotonic operation token, and only the current token may close the interaction or clear pending state after navigation, including A → B → A selection sequences.

Destinations are disabled while a move is pending. Escape and outside-click dismissal are ignored until the request settles, so reopening cannot submit a second concurrent move. Project inputs remain reactive while the picker is open: renames and area changes update visible labels, while removed or newly inbox-role projects disappear unless they are the active pending destination, which remains visible and disabled until its request settles.

The search field and destination buttons use normal Tab/Shift+Tab navigation rather than claiming listbox semantics. Opening the picker focuses search; Tab traverses every visible destination and the no-results state is announced as status text. Escape first clears a nonempty search, then closes the interaction and returns focus to **More actions** when search is empty and no move is pending. Outside click closes without restoring focus only when no move is pending, and changing the selected issue resets the visible interaction without invalidating the operation token.

## State and Errors

The sidebar owns only project-creation and area-collapse state. Removing inline rename also removes rename draft, focus, saving, cancellation, and error state from the sidebar.

The issue overflow menu owns whether its normal actions or destination picker are visible and resets that state when the selected issue changes. Existing project-loading, project-creation, daemon, and issue-mutation error behavior otherwise remains unchanged.

## Acceptance Criteria

- Kata areas use the shared grouped section and scroll-area behavior without changing system-view or project ordering.
- Project navigation, active selection, open counts, project creation, and existing status/error presentation still work.
- No sidebar control, double-click, or keyboard path starts project renaming.
- The rename client and store operations remain available.
- The issue breadcrumb displays the current project name or existing UID fallback as passive text.
- Issue reassignment is available only through **More actions → Move to another project**, excludes ineligible destinations, and disambiguates duplicate names.
- Successful reassignment closes the menu; handled failure surfaces the workspace error and remains retryable.
- The overflow interaction uses normal dialog/button keyboard navigation at desktop and narrow widths, with the specified two-stage Escape behavior.

## Testing

Component tests will cover area ordering, default expansion, collapse and expansion, collapse-state lifetime, project navigation, project creation, and the absence of sidebar rename controls and inline rename behavior.

Issue-detail and overflow-menu component tests will prove that the project breadcrumb is passive, its fallback text remains correct, destination names are disambiguated, the move action is hidden without destinations, success closes the picker, failure remains retryable, and keyboard dismissal restores focus. Store and client reassignment tests remain unchanged.

Full-stack Playwright coverage will replace the existing pencil-button and double-click rename scenarios. The replacement must prove project navigation and creation still work and that neither rename affordance is available. The existing real-daemon move scenario will be migrated to **More actions**, including successful keyboard selection and a forced daemon failure that surfaces the workspace error, leaves the picker open, and succeeds on retry. Existing browser coverage for grouped-sidebar scrolling remains the geometry-level check for the shared overlay indicator.

## Implementation Order

1. Migrate sidebar scrolling and area groups while preserving navigation and creation behavior.
2. Remove sidebar rename state, controls, callback wiring, and contradictory tests.
3. Add overflow-menu reassignment and update both workspace callbacks to return success. This intermediate commit intentionally leaves the existing breadcrumb control available so reassignment is never removed before its replacement lands.
4. Make the issue breadcrumb passive immediately afterward, then update component tests for both interactions.
5. Replace the obsolete full-stack rename scenarios and run the affected frontend suites.

## Scope

This change does not remove Kata's rename or move APIs, add a project-management page, modify daemon behavior, or change backend and database contracts.
