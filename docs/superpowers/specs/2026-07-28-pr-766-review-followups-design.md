# PR 766 Review Follow-ups Design

## Goal

Close the actionable findings from roborev-ci reviews `75f292a` and `b234725`, and keep the real-backend Firefox E2E suite reliable without weakening its behavioral assertions.

## Dragging terminal sessions into detail panes

Terminal-session drags will publish both the existing runtime-session payload and the workspace-scoped tabbed-panel payload already used by workflow drags. The detail-pane drag scope and session-to-pane-key mapping will flow through `WorkspaceTerminalView`, `DockedTerminalPanel`, and recursive `TerminalSplitTree` nodes. Every drag completion path will clear both payloads.

This keeps terminal rearrangement behavior intact while allowing leaf-header and selector drags to target detail panes through the established pane-drop protocol.

## Promoting sessions when the workspace pane is retired

Session promotion will choose an on-screen split anchor in this order:

1. the workspace pane;
2. a promoted session for the same workspace;
3. the active or first visible detail pane.

The third case supports row-only layouts, where no workspace pane remains. Promotion will still fail safely when the layout has no visible anchor.

## Control and overlay ownership

When the workspace pane is retired, the external terminal dock header will be the sole owner of workspace strip actions, including Delete. Internal dock headers will not render those actions, and promoted terminal panes will retain their controls trigger without duplicating strip actions.

The terminal host's activation signal will remain tied to host visibility. A separate interaction-visibility signal will allow portalled launcher and confirmation dialogs to open from controls rendered outside the parked host. This prevents background terminal activation while restoring Launch, Rename, Stop, Delete, and force-delete interactions from row-only and promoted-pane controls.

## Firefox timeout

The pooled-server option test exercises three complete server lease/reset cycles. It will use Playwright's slow-test budget while preserving every assertion. Chromium and Firefox behavior remains identical; only this infrastructure-heavy test receives additional time under contended Firefox CI workers.

## Verification

Regression coverage will prove:

- terminal drags publish and clear the scoped detail-pane payload;
- row-only session promotion creates a visible promoted pane;
- the external dock owns strip actions when the workspace pane is retired;
- launcher and destructive dialogs can open from externally rendered controls without activating the parked terminal host;
- the pooled-server test passes under the Firefox real-backend configuration.

Focused component and store tests will run before the affected Playwright checks. Modified Svelte files will pass the Svelte autofixer and the repository's frontend verification.
