# VS Code Workflow Panel Interaction Spec

Primary source: local VS Code checkout at `/Users/mariusvniekerk/src/microsoft/vscode`, revision `6b1e5513a8b`.

This spec describes the VS Code interaction model to mirror for middleman's workflow and terminal panel work. It intentionally focuses on editor groups/tabs and integrated terminal tabs/splits, not VS Code implementation internals that do not map to middleman.

## Mental Model

VS Code has two related but distinct tab models:

- **Editor area:** an `EditorPart` owns a grid of editor groups. Each group owns an ordered editor model and a tab strip. A group can be split, moved, copied, merged, focused, or persisted as part of the editor grid. Source: `src/vs/workbench/browser/parts/editor/editorPart.ts:476`, `src/vs/workbench/browser/parts/editor/editorPart.ts:532`, `src/vs/workbench/browser/parts/editor/editorPart.ts:834`.
- **Terminal panel:** a `TerminalGroupService` owns an ordered list of terminal groups. Each terminal group is represented as one terminal tab entry in the terminal tabs list; a group may contain multiple split terminal instances. Source: `src/vs/workbench/contrib/terminal/browser/terminalGroupService.ts:97`, `src/vs/workbench/contrib/terminal/browser/terminalGroup.ts:334`, `src/vs/workbench/contrib/terminal/browser/terminalGroup.ts:489`.

For middleman, use the same separation:

- A **workflow group** is a top-level pane in the workspace/workflow area.
- A **workflow tab** is an item inside a workflow group.
- A **terminal group** is a tab in the docked terminal panel.
- A **terminal split** is a terminal instance inside the active terminal group.

Do not collapse terminal tabs and terminal splits into one flat model. VS Code treats "tab order" and "split order inside a tab" as different operations.

## Editor Groups And Tabs

### Active Group And Active Tab

When an editor group receives focus, VS Code makes that group active, records it as most recently active, marks the previous group inactive, marks the new group active, and restores the group if it was minimized. Source: `src/vs/workbench/browser/parts/editor/editorPart.ts:654`, `src/vs/workbench/browser/parts/editor/editorPart.ts:692`.

Opening a tab decides both tab activity and group activation. An editor opens active unless explicitly inactive; activating a tab also activates/restores its group unless `preserveFocus` or explicit activation options say otherwise. Source: `src/vs/workbench/browser/parts/editor/editorGroupView.ts:1168`, `src/vs/workbench/browser/parts/editor/editorGroupView.ts:1192`.

Middleman behavior:

- Clicking a workflow tab makes that tab active and makes its group active.
- If focus is intentionally preserved, still update selection and visual active state, but avoid stealing DOM focus from the originating control.
- When a group becomes inactive, clear multi-selection down to the active tab to avoid stale multi-drag state. VS Code does this when a group is deactivated. Source: `src/vs/workbench/browser/parts/editor/editorGroupView.ts:937`.

### Tab Click, Selection, Pinning

VS Code tabs are draggable elements with `role="tab"`. Source: `src/vs/workbench/browser/parts/editor/multiEditorTabsControl.ts:821`.

Click behavior:

- Primary click opens the clicked tab, activates the group, and focuses the tab control as needed. Source: `src/vs/workbench/browser/parts/editor/multiEditorTabsControl.ts:889`.
- Shift-click selects a range from an anchor tab. Cmd/Ctrl-click toggles a tab in the selection. Source: `src/vs/workbench/browser/parts/editor/multiEditorTabsControl.ts:907`.
- Double-clicking an unpinned tab pins it; double-clicking a pinned tab may expand/maximize the group depending on settings. Source: `src/vs/workbench/browser/parts/editor/multiEditorTabsControl.ts:1049`.
- Double-clicking empty tab-strip space creates a new pinned editor at the end. Source: `src/vs/workbench/browser/parts/editor/multiEditorTabsControl.ts:299`.

Middleman behavior:

- Support click-to-activate, shift range selection, and Cmd/Ctrl toggle for workflow tabs if multi-tab moves are implemented.
- Treat "pinning" as optional. If middleman has no preview-tab concept, do not invent one; just keep tab ordering and activation semantics.
- Empty tab-strip double-click should create a sensible new workflow item only if the product has an obvious creation action. Otherwise omit it.

### Reorder Within A Group

Dragging a tab inside the same group moves it to the drop index. VS Code computes a target index, adjusts it when moving forward within the same source group, then calls `moveEditor` with the new index. Source: `src/vs/workbench/browser/parts/editor/multiEditorTabsControl.ts:2225`, `src/vs/workbench/browser/parts/editor/multiEditorTabsControl.ts:2270`.

The model update pins a moved editor and forwards the move to the title control so the tab strip redraws in-place. Source: `src/vs/workbench/browser/parts/editor/editorGroupView.ts:1400`.

Middleman behavior:

- During drag, show an insertion target in the tab strip.
- On drop in the same group, reorder selected tabs as a stable block and preserve their relative order.
- After drop, keep the dragged tab or first dragged tab active and focus the target group.

### Drag Between Groups

VS Code distinguishes moving/copying editors from moving/copying whole groups:

- Dropping on a tab strip merges dragged editors or a dragged group into the target group at the target index. Source: `src/vs/workbench/browser/parts/editor/multiEditorTabsControl.ts:2237`, `src/vs/workbench/browser/parts/editor/multiEditorTabsControl.ts:2256`.
- Dropping in the editor content area can split into a new group or merge into the current group depending on the drop zone. Source: `src/vs/workbench/browser/parts/editor/editorDropTarget.ts:251`.
- Cross-group moved editors are always pinned and preserve view state where possible. Source: `src/vs/workbench/browser/parts/editor/editorGroupView.ts:1437`.

Middleman behavior:

- Dropping a workflow tab onto another group's tab strip inserts into that group at the indicated index.
- Dropping a workflow tab into a group's content drop zone either merges into that group or creates a new group based on the zone.
- If a move empties the source group and the middleman setting is "close empty groups", remove that source group and activate the nearest previous group.

### Split And Drop Zones

VS Code shows a full-group overlay while dragging over an editor group. The center means "merge into this group"; edges mean "split". Source: `src/vs/workbench/browser/parts/editor/editorDropTarget.ts:144`.

Drop-zone rules:

- Split directions are `UP`, `DOWN`, `LEFT`, `RIGHT`. Source: `src/vs/workbench/services/editor/common/editorGroupsService.ts:41`.
- With splitting enabled, editor drags use a 10% edge threshold; group drags give the preferred split direction a larger 30% threshold. Source: `src/vs/workbench/browser/parts/editor/editorDropTarget.ts:385`.
- The actual split choice uses thirds for left/right/up/down; the center resolves to merge. Source: `src/vs/workbench/browser/parts/editor/editorDropTarget.ts:413`.
- The overlay is half-width or half-height for split directions, and full-size for merge. Source: `src/vs/workbench/browser/parts/editor/editorDropTarget.ts:471`.
- When tabs are visible, the content area below the tab strip is the group drop target; if the group is empty or tabs are hidden, the full group area is targetable. Source: `src/vs/workbench/browser/parts/editor/editorDropTarget.ts:525`.

Middleman behavior:

- Implement one visible overlay per hovered workflow group.
- Center drop merges into the hovered group.
- Edge drop creates a sibling split group in the indicated direction.
- Use a simple version of VS Code's zones: 10% edge threshold for tab/item drags, 30% on the preferred axis if dragging a whole group.
- Full-size merge overlay should not obscure the tab strip when tabs are visible.

### Layout Persistence

VS Code persists editor layout as a serialized grid plus active group and most-recent-active groups. Source: `src/vs/workbench/browser/parts/editor/editorPart.ts:1406`.

Applying a layout counts required groups, merges surplus groups into the last group in the target layout, recreates the grid descriptor, and restores focus if needed. Source: `src/vs/workbench/browser/parts/editor/editorPart.ts:476`.

Middleman behavior:

- Persist group tree orientation, group sizes, tab order, active group id, active tab id per group, and most-recent-active group order.
- Restore layout before attaching expensive terminal/process state.
- If persisted groups are invalid or missing, degrade to one group rather than failing the whole workspace.

## Terminal Tabs And Splits

### Terminal Group Model

The terminal tab list represents terminal groups, while each group owns ordered terminal instances. `TerminalGroupService.activeGroup` derives from `activeGroupIndex`; `activeInstance` derives from the active group's active instance. Source: `src/vs/workbench/contrib/terminal/browser/terminalGroupService.ts:97`.

Creating the first group/instance automatically activates it. Source: `src/vs/workbench/contrib/terminal/browser/terminalGroupService.ts:162`.

Middleman behavior:

- A terminal tab equals a terminal group.
- A split terminal is an instance inside a terminal group.
- Activate terminal group and terminal instance together when a terminal tab is clicked.

### Terminal List Visibility

VS Code's terminal tabs list is configurable and hides depending on count:

- Tabs disabled: never show.
- `hideCondition: never`: always show.
- `singleTerminal`: show when more than one terminal instance exists.
- `singleGroup`: show when more than one terminal group exists.
- Hidden chat terminals force visibility. Source: `src/vs/workbench/contrib/terminal/browser/terminalTabbedView.ts:168`.

The tab list is added or removed from a split view when visibility changes. It is not removed while the mouse context says the user is interacting with the tabs. Source: `src/vs/workbench/contrib/terminal/browser/terminalTabbedView.ts:196`.

Middleman behavior:

- Default: show the terminal tab list when there is more than one terminal group or the active group has splits.
- Do not make the terminal list flicker closed while the pointer is over it.
- Keep the terminal content view as the high-priority split-view child and the tab list as lower priority. VS Code does this in its split view setup. Source: `src/vs/workbench/contrib/terminal/browser/terminalTabbedView.ts:289`.

### Terminal Split Behavior

Adding a terminal instance to a non-empty group inserts it after the parent or active instance. If the split pane container is mounted, the new pane is inserted at the corresponding split index. Source: `src/vs/workbench/contrib/terminal/browser/terminalGroup.ts:302`.

Splitting creates a new panel terminal instance, adds it to the group, and makes it active. Source: `src/vs/workbench/contrib/terminal/browser/terminalGroup.ts:526`.

The split pane orientation follows terminal location and panel position: horizontal panel gives horizontal splits; side/vertical location gives vertical splits. Source: `src/vs/workbench/contrib/terminal/browser/terminalGroup.ts:480`, `src/vs/workbench/contrib/terminal/browser/terminalGroup.ts:537`.

Middleman behavior:

- Split active terminal creates a sibling terminal instance immediately after the active instance.
- The new split becomes active.
- Resizing split panes should maintain relative sizes and enforce a minimum pane size.
- If the docked terminal panel changes orientation, rotate/reflow split panes rather than destroying terminal instances.

### Terminal Tab Drag And Reorder

VS Code terminal drag behavior is list-based:

- Dragging a terminal tab attaches terminal resource data to the drag event. Source: `src/vs/workbench/contrib/terminal/browser/terminalTabsList.ts:653`.
- Hovering a target terminal for 500ms auto-focuses it. Source: `src/vs/workbench/contrib/terminal/browser/terminalTabsList.ts:675`.
- Dropping with no target moves the dragged group(s) to the end. Source: `src/vs/workbench/contrib/terminal/browser/terminalTabsList.ts:757`.
- Dropping on a target calls `moveGroup`, then activates the first source instance and selects the target group in the list. Source: `src/vs/workbench/contrib/terminal/browser/terminalTabsList.ts:768`.

`moveGroup` has two modes:

- If source and target are in the same terminal group, reorder instances within that split group. Source: `src/vs/workbench/contrib/terminal/browser/terminalGroupService.ts:354`.
- If source and target are in different groups, reorder whole groups around the target group. Source: `src/vs/workbench/contrib/terminal/browser/terminalGroupService.ts:367`.

Middleman behavior:

- Dragging a terminal tab moves terminal groups, not individual split panes, unless the UI explicitly targets a split row.
- Dropping a split terminal onto another split in the same group reorders split panes.
- Dropping onto another group should move the whole source terminal group unless middleman provides an explicit "move split into group" affordance.
- After drop, activate the first dragged terminal and keep the terminal tabs list selection in sync.

### Terminal Rename Behavior

VS Code exposes terminal rename through command/menu actions. `RenameActiveTab` uses F2 on Windows/Linux and Enter on macOS when the terminal tabs list has focus. Source: `src/vs/workbench/contrib/terminal/browser/terminalActions.ts:769`.

Inline rename is only used for normal tab-list rename. If the action originates from an inline tab menu, VS Code falls back to a quick-pick rename flow. Source: `src/vs/workbench/contrib/terminal/browser/terminalActions.ts:791`.

Inline rename behavior:

- Store editable state on a terminal editing service. Source: `src/vs/workbench/contrib/terminal/browser/terminalEditingService.ts:23`.
- Render an input box in the tab row, focus it, and select the existing title. Source: `src/vs/workbench/contrib/terminal/browser/terminalTabsList.ts:439`.
- Enter commits if valid; Escape cancels; blur commits if valid. Source: `src/vs/workbench/contrib/terminal/browser/terminalTabsList.ts:466`.
- Empty title resets to generated title. Source: `src/vs/workbench/contrib/terminal/browser/terminalInstance.ts:2392`.

Middleman behavior:

- Provide F2 rename for focused terminal tab; Enter rename on macOS is optional unless the tab list owns focus semantics.
- Inline rename should focus an input, select the current name, commit on Enter/blur, cancel on Escape.
- Empty submitted name should clear the custom name and fall back to generated terminal title.

### Terminal Layout Persistence

VS Code stores terminal group layout as tabs with `isActive`, `activePersistentProcessId`, split terminal ids, and relative split sizes. Source: `src/vs/workbench/contrib/terminal/browser/terminalGroup.ts:341`.

The terminal service recreates groups from persisted tab layouts, restores the active group, recreates splits by creating later terminals with the previous terminal as parent, then reapplies relative pane sizes. Source: `src/vs/workbench/contrib/terminal/browser/terminalService.ts:497`.

Runtime state is saved debounced and skipped during shutdown; the saved state contains terminal tabs and background terminal ids. Source: `src/vs/workbench/contrib/terminal/browser/terminalService.ts:723`.

Middleman behavior:

- Persist terminal group order, active group, active instance per group, instance ids, custom titles, and relative split sizes.
- Treat persisted process/session identity separately from UI layout. If a process cannot be restored, keep the UI layout recoverable.
- Save terminal layout after tab/split reorder, split creation/removal, rename, and resize, but debounce rapid resize writes.

## Mapping To Middleman

| VS Code concept | Middleman concept | Implementation note |
| --- | --- | --- |
| `EditorPart` serialized grid | Workspace workflow layout | Persist nested split tree, sizes, active group, MRU group order. |
| Editor group | Workflow group/pane | Owns ordered workflow tabs and active tab id. |
| Editor tab | Workflow tab/item | Reorder in group; drag across groups; activate on click. |
| Editor content drop overlay | Workflow group drop overlay | Center merge, edges split. |
| Terminal group | Terminal tab | Top-level terminal tab list row. |
| Terminal instance | Terminal split pane | Ordered child of a terminal group. |
| Terminal tab list | Docked terminal tab rail/list | Show based on group/split count; keep selection synced with active terminal. |
| Terminal layout info | Terminal panel layout state | Persist tab order, split order, active ids, relative sizes. |

## Implementation Checklist

- Model workflow groups and terminal groups separately; do not flatten terminal splits into terminal tabs.
- Add DnD state that records source type: workflow tab, workflow group, terminal group, or terminal split.
- Implement workflow drop overlay with center merge and edge split zones.
- Keep active group/tab updates explicit after click, drop, split, close, and restore.
- Persist workflow split tree and terminal group/split layout independently.
- Implement terminal tab list visibility rules that avoid pointer-time flicker.
- Implement terminal split creation after the active split and activate the new split.
- Implement terminal rename with inline input, Enter/blur commit, Escape cancel, and empty-name reset.
- Add focused e2e tests for tab reorder, drag between groups, edge split drop, terminal split activation, terminal tab reorder, terminal rename, and layout restore.
