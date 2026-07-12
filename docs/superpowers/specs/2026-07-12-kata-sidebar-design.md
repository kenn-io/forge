# Kata Sidebar Design

## Goal

Make Kata navigation use the shared grouped-sidebar behavior introduced by PR #662 and remove conspicuous project renaming from the sidebar.

## Sidebar Structure

`KataSidebar` will render its scrollable navigation through `SidebarScrollArea`. Each Kata area will use `GroupedSidebarSection`, matching the grouping, selection, collapse, and scrolling behavior of the pull request, issue, and workspace rails.

Existing project selection, project status information, task counts, error indicators, and project creation remain available. Project rows will use the shared sidebar styling contract rather than maintaining a parallel set of group and row styles.

## Project Renaming

Project renaming will be removed from the current UI. The sidebar will no longer contain a pencil button, respond to project-row double-clicks by entering rename mode, or render an inline rename form.

`KataWorkspace` will stop forwarding a rename callback to the sidebar. The underlying Kata client and workspace-store rename operations will remain intact so a future deliberate project-management surface can use them without restoring easy sidebar renaming.

## State and Errors

The sidebar will own only navigation-related state, including area collapse state required by the shared grouping abstraction. Removing inline rename also removes rename draft, focus, saving, cancellation, and error state from the sidebar.

Existing project-loading, project-creation, and daemon error behavior remains unchanged.

## Testing

Component tests will cover area grouping, collapse and expansion, project navigation, project creation, and the absence of sidebar rename controls and inline rename behavior.

Workspace tests will continue to cover project-scope switching through Kata navigation. Existing full-stack Playwright scenarios that require pencil-button or double-click renaming will be removed or replaced with an assertion that renaming is unavailable from the sidebar. Shared scroll-indicator behavior will continue to be exercised in the browser lane where geometry matters.

## Scope

This change does not remove Kata's rename API, add a replacement rename interface, modify daemon behavior, or change backend and database contracts.
