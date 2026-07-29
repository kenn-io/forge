# Workspace Header Launch Session Design

## Goal

Make launching another workspace session a one-click action from the workspace pane header, including when the terminal body or controls popover is closed.

## Design

Add a compact, icon-only Launch session action immediately before the existing Delete workspace action in the workspace pane header. Use the existing play icon, tooltip, and accessible name so the new shortcut matches the surrounding 24-pixel header controls without widening the action cluster.

The existing Launch session action remains in the workspace controls popover. Promoted session panes intentionally omit the workspace-level header actions, so retaining the popover action preserves launch access from those panes while the workspace pane gains the requested shortcut.

The shortcut uses the existing launcher-opening behavior. It does not launch a default target directly, change workspace or session identity, alter the controls popover, or add a new API path.

## States and Errors

- Show the header shortcut only when the workspace header already shows Delete workspace: the workspace is loaded, ready, and the pane owns workspace-level strip actions.
- Disable the shortcut whenever other workspace actions are blocked.
- Clicking it opens the existing Launch a session overlay. Existing launcher state and error handling remain authoritative.
- Render Launch session before Delete workspace so the destructive action keeps its established position at the end of the workspace-specific action group.

## Verification

Add a component regression test that renders an embedded ready workspace, verifies the header exposes Launch session beside Delete workspace without opening the controls popover, and verifies clicking it opens the existing Launch a session dialog. Run the focused component test, the full frontend test suite, Svelte analysis, and the affected workspace launcher Playwright suite.
