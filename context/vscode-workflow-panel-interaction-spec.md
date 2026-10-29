# VS Code Workflow Panel Interaction Spec

Use this document when changing kenn-forge's workflow groups, workflow tabs,
terminal groups, terminal splits, or related panel interaction semantics.

kenn-forge mirrors VS Code's editor-group and integrated-terminal interaction
model, not its implementation internals. Primary source: `microsoft/vscode`
revision `6b1e5513a8b`; source paths below are in that tree.

## Mental Model

- A **workflow group** is a top-level pane in the workspace/workflow area, and a
  **workflow tab** is an item inside it, like VS Code editor groups and tabs
  (`src/vs/workbench/browser/parts/editor/editorPart.ts`).
- A **terminal group** is a tab in the docked terminal panel, and a **terminal
  split** is an instance inside the active terminal group, like VS Code terminal
  groups (`src/vs/workbench/contrib/terminal/browser/terminalGroupService.ts`).
- Model workflow groups and terminal groups separately, and never flatten
  terminal splits into terminal tabs: tab order and split order inside a tab are
  different operations.

## Workflow Groups And Tabs

- Clicking a workflow tab makes that tab and its group active. When focus is
  intentionally preserved, still update selection and active state, but do not
  steal DOM focus from the originating control.
- Keep active group/tab updates explicit after click, drop, split, close, and
  restore.
- When a group becomes inactive, clear its multi-selection down to the active
  tab to avoid stale multi-drag state, as VS Code does
  (`src/vs/workbench/browser/parts/editor/editorGroupView.ts:937`).
- If multi-tab moves exist, support click-to-activate, Shift range selection,
  and Cmd/Ctrl toggle.
- Pinning is optional; if kenn-forge has no preview-tab concept, do not invent
  one.
- Empty tab-strip double-click creates a new item only if the product has an
  obvious creation action; otherwise omit it.
- During drag, show an insertion target. A same-group drop reorders the
  selected tabs as a stable block, preserving their relative order; afterwards
  the first dragged tab stays active and the target group takes focus.
- Dropping a workflow tab on another group's tab strip inserts it at the
  indicated index; dropping into a group's content zone merges or splits by
  zone.
- If a move empties the source group and the "close empty groups" setting is
  on, remove that group and activate the nearest previous group.

### Drop Zones

- Show one overlay per hovered workflow group: the center merges into it, and
  an edge creates a sibling split group in that direction.
- Use VS Code's thresholds: 10% edges for tab/item drags, 30% on the preferred
  axis when dragging a whole group
  (`src/vs/workbench/browser/parts/editor/editorDropTarget.ts:385`).
- The full-size merge overlay must not obscure the tab strip when tabs are
  visible.

### Layout Persistence

- Persist the group tree orientation, group sizes, tab order, active group id,
  active tab id per group, and most-recent-active group order.
- Restore layout before attaching expensive terminal/process state.
- If persisted groups are invalid or missing, degrade to one group rather than
  failing the whole workspace.
- Persist the workflow split tree and the terminal group/split layout
  independently.

## Terminal Tabs And Splits

- Clicking a terminal tab activates the terminal group and its terminal
  instance together.
- Show the terminal tab list when there is more than one terminal group or the
  active group has splits. It must not flicker closed while the pointer is over
  it (`src/vs/workbench/contrib/terminal/browser/terminalTabbedView.ts:196`).
- The terminal content view is the high-priority split-view child and the tab
  list the lower-priority one.
- Splitting creates a sibling instance immediately after the active instance
  and makes the new split active.
- Resizing split panes keeps relative sizes and enforces a minimum pane size.
  If the docked panel changes orientation, reflow split panes rather than
  destroying terminal instances.
- Dragging a terminal tab moves terminal groups, not individual split panes,
  unless the UI explicitly targets a split row
  (`src/vs/workbench/contrib/terminal/browser/terminalGroupService.ts:354`).
- Dropping a split onto another split in the same group reorders split panes.
  Dropping onto another group moves the whole source group unless kenn-forge
  offers an explicit "move split into group" affordance.
- After a terminal drop, activate the first dragged terminal and keep the tab
  list selection in sync.
- F2 renames the focused terminal tab; Enter rename on macOS is optional unless
  the tab list owns focus semantics.
- Inline rename focuses an input with the current name selected, commits on
  Enter or blur, and cancels on Escape. An empty name clears the custom name
  and falls back to the generated terminal title.

### Terminal Layout Persistence

- Persist terminal group order, active group, active instance per group,
  instance ids, custom titles, and relative split sizes.
- Keep persisted process/session identity separate from UI layout; if a process
  cannot be restored, the layout stays recoverable.
- Save terminal layout after tab/split reorder, split creation or removal,
  rename, and resize, debouncing rapid resize writes.
