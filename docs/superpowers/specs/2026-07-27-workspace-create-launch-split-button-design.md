# Workspace Create-and-Launch Split Button

## Purpose

Replace persistent default-agent auto-launch with an explicit launch choice at
workspace creation time. Every workspace creation surface uses the same split
control:

```text
[ Create Workspace | chevron ]
```

The primary segment creates a workspace without launching a runtime session.
The chevron opens a menu of configured agent launch targets. Choosing an agent
creates the workspace and launches that agent as soon as the workspace is
ready, without a second confirmation dialog.

This applies consistently to pull request, issue, ad-hoc, and Kata workspace
creation. Existing capitalization may remain contextual (`Create Workspace`
in detail actions and `Create workspace` in dialog footers), but behavior and
menu copy are shared.

## Interaction

A reusable UI component owns the split control, menu, and accessibility
behavior. It accepts the current create label and state, configured launch
targets, a create-only callback, and a create-and-launch callback.

- The primary segment retains the current create-only behavior.
- The chevron exposes `aria-haspopup="menu"` and `aria-expanded`.
- The menu has the accessible name `Create and launch` and lists visible targets whose kind
  is `agent`, including enabled custom agents, without repeating that name as visible chrome.
- Available agents are selectable. Unavailable agents remain visible but
  disabled and expose their existing disabled reason.
- Shell and non-agent run configurations are not creation choices; they remain
  available from the workspace's ordinary Launch menu.
- Selecting an agent closes the menu before creation starts.
- The popup uses `role="menu"` and each target uses `role="menuitem"`.
  Opening it focuses the first enabled target; Arrow Up/Down, Home, and End
  move among enabled targets, Enter or Space selects, Escape closes and
  restores focus to the chevron, and Tab closes without trapping focus.
- An outside press closes the menu.
- Both segments and menu actions are disabled while that workspace creation is
  pending. Whenever the existing create action is blocked, both the primary
  segment and chevron are disabled with the same reason. Selecting an item
  cannot submit twice.
- Narrow detail panes and drawers retain both segments. The primary segment
  may use the surface's existing short label, while the chevron keeps its
  accessible name; the floating menu must not be clipped by the drawer or
  split-pane boundary.

The shared component is used by:

- pull request detail `Create Workspace`;
- issue detail `Create Workspace`;
- the ad-hoc `New workspace` dialog submit action; and
- Kata workspace creation.

## Launch Intent

Launch intent is an explicit, session-scoped command, not a preference. The
frontend workspace-creation lifecycle store keeps a map from workspace ID to
the selected target key. Creation without a target adds no entry.

Every create handler carries an optional target key through its complete
workflow. This includes issue and ad-hoc branch-conflict resolution: choosing a
suggested branch, reusing a branch, or reusing an existing directory preserves
the original launch choice. Canceling the conflict flow discards the choice.

When a create response returns a workspace ID, an explicit selected target is
queued for that ID regardless of whether the response reports a freshly
created branch or a reused branch. The deliberate menu choice supplies the
authorization that persistent auto-launch previously lacked. Idempotent or
racing responses remain safe because the terminal view consumes each queued
intent once and does not launch a second session when the workspace already has
runtime sessions.

The pending-intent collection is reactive: enqueue, consume, and reset publish
a new collection value. The terminal launch effect reads the lookup so
enqueueing an intent invalidates it even when branch or directory reuse returns
a workspace that is already ready and whose runtime state is already live.

The pending intent remains in memory while workspace setup runs and while the
persistent terminal host moves between layouts. It is not written to config,
SQLite, local storage, or the workspace creation HTTP request. A page reload
therefore clears an unconsumed intent, matching other one-shot UI commands.

## Runtime Launch

When the selected workspace first becomes ready and runtime state is live, the
terminal view checks for a queued target:

1. If the workspace already has a runtime session, consume the intent without
   launching another.
2. Resolve the target key against the workspace's runtime launch targets.
3. If the target is absent, is not an agent, or became unavailable, consume
   the intent and show one danger flash with the existing disabled reason when
   available.
4. Otherwise consume the intent and call the existing manual launch path.

This launch is equivalent to opening the ready workspace's Launch menu and
choosing the agent. The creation-menu choice is already an explicit request to
execute that target, so pull request workspaces do not show an additional
confirmation modal and do not use the default-auto-launch-only
same-repository-head authorization flag. Server-side runtime launch validation
and current target availability checks remain authoritative.

This deliberately permits create-and-launch for fork and unresolved-head pull
requests. One explicit agent menu choice can therefore execute repository
hooks from contributor-controlled code before the maintainer has inspected the
checkout, with access to the same local credentials as a manual Launch-menu
choice. The accepted boundary is deliberate action at the point of execution:
the primary create-only segment remains safe, while the agent-labelled menu
item is treated as the same trust decision as manually launching that agent in
an existing fork workspace.

The old auto-launch confirmation state and modal are deleted. No code path
launches an agent merely because a workspace was created.

`require_same_repo_head` was introduced only for the default-auto-launch
confirmation path and has no caller after this change. Delete the request
field, frontend launch option, generated API fields, flag-gated server
authorization branches, the now-unused untrusted-head conflict helper, and
their dedicated tests. Keep the shared head-repository snapshot and
agent-context machinery that still protects repository classification and
serves non-default-launch callers.

## Agent Target Hydration

`GET /settings` continues returning `launch_targets`; this change adds
hydration of those targets into the shared settings store so every creation
surface can render the same menu before a workspace exists. The runtime
endpoint remains the final source of truth after creation because
repository-specific hooks and availability may differ from the settings-level
inventory.

The creation control filters the settings inventory to visible agent targets.
The terminal view repeats the kind and availability checks against runtime
state before launching, so stale settings cannot execute a removed, disabled,
or reclassified target.

## Removing `default_agent`

Persistent default-agent behavior is removed rather than retained as an inert
compatibility path. This is a pre-merge rework of PR #748:
`default_agent` is absent from `origin/main` and has never been a released
configuration contract, so no user migration or backward-compatibility path is
required.

- delete `default_agent` from the TOML config model and validation;
- delete it from settings GET and update request schemas;
- regenerate OpenAPI, TypeScript, and Go clients without the field;
- remove default-agent hydration and mutation from frontend stores;
- remove the settings dialog control and save behavior;
- remove default-agent documentation, examples, and tests; and
- remove all auto-launch naming and one-shot state that inferred intent from
  creation alone.

Development configurations created while testing the unmerged branch receive
no special migration; the normal config decoder behavior for an unknown field
applies. No alias, migration shim, or hidden fallback is introduced.
`launch_targets` remains in the settings response because it is the inventory
for the explicit dropdown.

## Error Handling

Workspace creation errors remain on their current surfaces. Branch conflicts
continue through their existing resolution UI while retaining the selected
target. The shared split control does not add a separate error surface.

Launch failures after successful creation do not make workspace creation look
failed. The workspace remains selected and usable, and the existing launch
error or an unavailable-target flash explains why no session started.

If a component unmounts or its selected item changes while creation is in
flight, the existing identity-scoped creation record still publishes the
workspace. The selected target is queued alongside that confirmed workspace ID
before presentation liveness checks, so layout and selection churn cannot
silently discard an explicit launch request.

## Verification

- Shared component tests cover primary creation, target selection, filtering,
  disabled targets, busy state, outside dismissal, Escape, and keyboard focus.
- Workspace-creation lifecycle store tests prove target keys are kept per
  workspace, consumed exactly once, and reset between tests.
- Pull request, issue, ad-hoc dialog, and Kata component tests prove their
  primary actions create only and their dropdown choices preserve the selected
  target through successful creation.
- Issue and ad-hoc conflict tests prove retry and reuse actions retain launch
  intent while canceling does not.
- Terminal view tests prove a queued target launches without a confirmation
  modal, missing or unavailable targets flash once, existing sessions suppress
  duplicate launch, ordinary creation never launches, and enqueueing against an
  already-ready workspace still triggers the effect.
- Pull request coverage proves an explicit create-and-launch choice works for a
  fork or unresolved head without `require_same_repo_head`, while the primary
  create-only action starts no session.
- Settings/config/API tests prove `default_agent` is absent and
  `launch_targets` still hydrates the creation menus.
- API generation is run and checked-in OpenAPI, TypeScript, and Go artifacts
  are reviewed for removal of `default_agent` and
  `require_same_repo_head`, with no unrelated contract drift.
- One full-stack Chromium test creates a pull request workspace through an
  agent dropdown choice and observes the runtime session start with no
  confirmation dialog.
- Svelte autofix analysis runs on every changed component, followed by focused
  component tests, affected Go tests with shuffle enabled, frontend checks, and
  the repository's broader verification required by the final blast radius.
