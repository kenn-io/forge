# Sidebar organization-name visibility

## Goal

Make organization-name visibility controllable from the PR, issue, and workspace sidebars so maintainers can adjust repository-label density where they are working.

## Behavior

- The PR filter menu includes a **Hide org name** visibility item.
- The issue filter menu includes the same item.
- Both items use the existing persisted `middleman:hideOrgName` preference already used by activity and provider-item rendering.
- The workspace view menu labels its existing inverse control **Hide org name** and keeps using the workspace list's existing persisted display preference.
- Selecting an item immediately updates repository labels in that sidebar.
- PR and issue compact filter menus include the visibility item. Their changed-state indicator and reset behavior account for it.
- The issue visibility item remains reachable in both compact and expanded sidebar layouts.
- Responsive focus and mobile PR/issue lists use the same preference and collision-safe labels.
- Existing sidebar layout, grouping, state filters, and workspace sort behavior remain unchanged.

## Components and data flow

`PullList.svelte` and `IssueList.svelte` add a Visibility section to their filter-section definitions. The item reads `grouping.getHideOrgName()` and toggles it through `grouping.setHideOrgName()`.

PR and issue lists format their group headers and per-item repo chips through the shared collision-safe repository-label formatter, so no new store or persistence key is needed.

Repository chip color remains keyed to the full provider, host, and repository identity rather than the shortened display label.

`WorkspaceListSidebar.svelte` continues to use `displayOptions.showOrgNames`; only the menu-facing label, identifier, description, and active-state polarity change. This avoids coupling workspace display preferences to the global PR/issue/activity preference.

## Accessibility and interaction

The existing `FilterDropdown` menu semantics, keyboard handling, focus behavior, and active checkmark remain the interaction contract. The active state means the stated action is enabled: a checked **Hide org name** item corresponds to hidden organization names.

## Testing

Focused component tests will verify that:

- PR and issue filter menus expose **Hide org name**.
- Selecting it toggles the shared preference and updates visible repository labels.
- Workspace exposes the renamed control with the correct active polarity and still updates grouped and flat labels.
- Compact-menu changed-state/reset behavior includes the PR and issue visibility preference.

The affected frontend test suite and Svelte validation will run after the final edit.
