# Terminal focus on launch + default agent auto-launch

Date: 2026-07-25
Status: Approved

## Problem

Two friction points in terminal workspaces mode:

1. Clicking a launch card (agent tile) to open an agent such as Claude or
   Codex mounts and connects the terminal, but keyboard focus stays on
   `document.body`. The user must click inside the pane before typing.
   Nothing in the codebase calls `term.focus()` today.
2. Creating a workspace always requires a second manual step: after the
   workspace is created and the user lands on `/terminal/{id}`, they must
   click a launch card to start their agent, even when they always launch
   the same one.

## Part 1: Focus terminal when it becomes active

### Mechanism

- Thread an `active` (visibility) prop from `WorkspaceTerminalView` through
  `TerminalPane` into both renderers (`XtermTerminalPane`,
  `GhosttyTerminalPane`). The prop is true when the pane belongs to the
  currently selected workspace tab and is visible.
- Each renderer focuses its input surface:
  - on the `false → true` transition of `active`, and
  - on initial terminal creation while `active` is already true. This
    covers `XtermTerminalPane`'s deferred init (the `Terminal` instance is
    constructed asynchronously after mount behind a font-load wait), so a
    focus request that arrives before the terminal exists is applied once
    init completes.
- xterm renderer calls `term.focus()`; Ghostty renderer focuses its surface
  element (or container if the embedding exposes no focus API).

### Behavior

- Launch flow: `handleLaunch` mounts the session terminal and selects its
  tab; the newly mounted pane initializes while active and focuses itself.
- Reopening an existing session tile and switching back to an existing
  terminal tab focus the terminal by the same transition.
- No focus stealing: the signal only changes when the user explicitly
  selects a tab or launches a session.

## Part 2: Default agent auto-launch on workspace creation

### Setting

- New optional config key `default_agent` (string), validated against
  launch target keys of kind `agent` — built-ins plus custom `[[agents]]`
  entries. Non-agent keys (e.g. `shell`), unknown keys, and disabled agents
  are rejected at config validation.
- Exposed in `GET /settings` and updatable via the settings update
  endpoint, alongside the existing agents payload.
- Editable in the Agent Settings UI as a "Launch automatically when
  creating a workspace" dropdown (options: None + agent launch targets).

### Trigger (frontend-only)

- In `WorkspaceTerminalView`: when the runtime for a *just-created*
  workspace transitions to ready with zero sessions, call the existing
  `handleLaunch(defaultAgentKey)`.
- "Just-created" is tracked via the existing `workspace-create-pending`
  store; the record is consumed so auto-launch fires at most once per
  creation. Opening an old empty workspace never auto-launches.
- Reusing `handleLaunch` means session mount, tab selection, and the
  Part 1 focus behavior all apply to the auto-launched session.
- Applies to all three creation entry points (issue detail, PR detail,
  Kata task), since they all navigate to `/terminal/{id}`.

### Fallbacks

- If `default_agent` is unset, or the target is missing, unavailable
  (binary not found), or disabled at runtime, skip auto-launch silently
  and land on the Home tab exactly as today.
- Manual launch cards are unchanged.

### Explicitly out of scope

- No atomic create+launch API. The create endpoints
  (`POST /workspaces`, issue workspace, Kata workspace) are untouched.
- No per-repository or per-entry-point default; one global setting.

## Testing

- Vitest (jsdom, plus browser project where real focus semantics matter):
  - pane focuses on activation and after deferred init while active;
  - auto-launch fires once on ready for a just-created workspace;
  - no auto-launch for existing/empty workspaces or when the target is
    unavailable (mock API via `mockApiFetch.ts`).
- Go: config validation of `default_agent` (unknown key, disabled agent,
  non-agent key rejected) and settings round-trip through the API.
