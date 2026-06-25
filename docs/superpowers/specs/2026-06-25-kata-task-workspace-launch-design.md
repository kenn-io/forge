# Kata Task Workspace Launch

## Problem

Kata task detail should offer the same workspace affordance that provider issues
have, but only when middleman can map the selected Kata task to one tracked
repository unambiguously.

Kata tasks live in external Kata daemons, while middleman workspaces are tied to
middleman's provider repository identity and synced provider issues. The bridge
between the two domains must not turn Kata into a provider or make middleman
config the source of truth for Kata daemon/project definitions.

The common case should not need manual setup: watched repositories can already
have configured local clone paths, and those clones can carry `.kata.toml`
metadata that names the Kata project. Manual settings exist only for gaps or
ambiguity.

## Goals

- Show a `Create workspace` action on a Kata task detail when the task resolves
  to one tracked provider repository and one provider issue number.
- Show `Open workspace` instead when a middleman issue workspace already exists
  for that provider issue.
- Hide the action entirely when repository or issue mapping is missing,
  ambiguous, or points at an untracked repository.
- Prefer automatic project mapping from configured local clones with `.kata.toml`
  over manual setup.
- Add a settings surface for explicit Kata project to repository mappings.
- Keep Kata task data external to Kata daemons and workspace lifecycle data in
  middleman's existing workspace model.

## Non-Goals

- Creating workspaces for arbitrary Kata tasks that do not correspond to a
  synced provider issue.
- Moving Kata project or task records into middleman's SQLite schema.
- Scanning arbitrary filesystem paths for `.kata.toml`.
- Inferring provider issue numbers from task title text or labels alone.
- Supporting fleet-remote workspace creation from Kata tasks in this design.

## Recommended Approach

Use a server-side resolver. The UI passes the selected Kata task and daemon
context to middleman, and middleman returns either an actionable workspace target
or no target.

The resolver uses this precedence:

1. Manual mapping for the selected Kata daemon and project UID.
2. Manual mapping for any daemon and the selected project UID.
3. Automatic mapping from `.kata.toml` in exact configured repositories with a
   non-empty `worktree_base_path`.
4. No target when neither source yields exactly one repository.

Manual mappings are a fallback and override only the project they name. Automatic
discovery remains the default path because it follows the user's existing watched
repo clone setup.

## Alternatives Considered

### Frontend-Only Mapping

The frontend could combine Kata project metadata and settings data, then call the
existing issue workspace endpoint directly.

This is rejected because provider route construction, default-host handling,
workspace existence checks, and ambiguity rules already belong on the server.
Duplicating them in Svelte would create another place for provider identity bugs.

### Manual Mappings Only

Settings could require every Kata project to be mapped explicitly.

This is too much ceremony for the expected setup. The user already has watched
repositories and local clones with `.kata.toml`, so middleman should use those
before asking for manual configuration.

### Extend Existing Provider Issues Directly

Kata tasks could be treated as provider issues and reused through provider issue
components.

This is rejected because Kata is a first-class non-provider mode. Its task data
stays owned by external Kata daemons, and only the workspace launch target crosses
into provider repository identity.

## Repository Mapping

Middleman reads `.kata.toml` only from exact configured repositories where
`worktree_base_path` is set. Glob entries are skipped because a glob does not
name one local clone.

The `.kata.toml` parser accepts a small, explicit shape:

```toml
[project]
uid = "project-kata"
```

`uid` is the stable identity used for automatic matching. If a file is absent,
unreadable, malformed, or missing `project.uid`, that repository contributes no
automatic mapping.

If two repositories claim the same Kata project UID for the same daemon scope,
the resolver treats the mapping as ambiguous and returns no workspace target.
The UI should not show a disabled button for this state because the user asked
for the button to be absent when there is no clear mapping.

## Manual Settings

Settings gains a `Kata projects` section in the Workspace settings group, next
to workspace terminal, agents, and fleet settings. The surface lists current
mappings and allows adding, editing, and removing one mapping at a time:

- Kata daemon ID, optional; empty means the mapping applies to any daemon.
- Kata project UID.
- Provider.
- Platform host.
- Repository path.

The repository selector should be backed by configured watched repositories so a
manual mapping cannot point to an untracked repository accidentally. If the
underlying repository is later removed from middleman settings, the mapping is
kept but the resolver treats it as inactive until fixed or deleted.

Persist manual mappings in middleman config, not in Kata metadata, because they
describe middleman's local interpretation of external Kata projects.

Manual mapping validation rejects duplicate entries with the same daemon scope
and project UID. A daemon-specific entry and a global entry may coexist; the
daemon-specific entry wins for that daemon.

## Task Issue Mapping

A Kata workspace target requires both repository identity and provider issue
number.

The resolver reads the issue number from structured Kata task metadata, not from
display text. The accepted metadata shape is:

```json
{
  "provider_issue": {
    "number": 123
  }
}
```

If later Kata already emits a richer provider issue reference, middleman can
accept that as an additional input, but the resolver must still verify that the
repository side maps to exactly one watched repo. The mapped provider issue must
already be synced in middleman's DB; if it is not present, the resolver returns
no target.

## API Shape

Add a middleman API for resolving the selected task:

`POST /api/v1/kata/workspace-target`

Request body:

- `daemon_id`
- `project_uid`
- `issue_uid`
- `issue_metadata`

Response body:

- `available: false` when no button should render.
- `available: true` with repository identity, provider issue number, and
  optional existing `workspace` ref when an action can render.

The endpoint does not call the Kata daemon. It resolves only from the task data
the frontend already has, middleman settings, local clone `.kata.toml` files, and
middleman's synced provider DB.

Workspace creation continues to use the existing provider-aware issue workspace
endpoint. This keeps workspace lifecycle behavior, branch naming, duplicate
handling, and setup events in one place.

## Frontend Behavior

Kata detail loads a workspace target whenever the selected task changes. The
button is rendered in the existing detail action row:

- `Create workspace` when `available` is true and no workspace ref is returned.
- `Open workspace` when an existing workspace ref is returned.
- No button when `available` is false or the target request fails.

Clicking `Create workspace` calls the existing issue workspace endpoint for the
resolved provider repo and issue number, then navigates to the created workspace.
Clicking `Open workspace` navigates to `/terminal/{workspace_id}`.

Transient resolver errors should be surfaced as a small request error in the Kata
detail, not as a permanent task property. The button should avoid stale actions:
while a selected-task resolver request is in flight, the previous task's
workspace target is cleared.

## Error Handling

The resolver intentionally uses absence for non-actionable states:

- No project mapping.
- Ambiguous project mapping.
- Missing provider issue metadata.
- Provider issue not synced.
- Repository is no longer tracked.

Those cases return `available: false`. They are expected states, not user-facing
errors.

Unexpected filesystem, config, or database failures return the standard
middleman problem envelope with stable codes. The UI branches on response status
and code, not prose.

## Testing

Backend coverage:

- Automatic mapping succeeds from a configured exact repo with
  `worktree_base_path` and `.kata.toml`.
- Glob repos and repos without local clone paths do not participate in automatic
  mapping.
- Duplicate `.kata.toml` project UID claims are ambiguous and return
  `available: false`.
- Manual mapping resolves to a watched repository and overrides an automatic
  mapping for the same daemon/project.
- Missing provider issue metadata, unsynced provider issue, and removed watched
  repo mappings return `available: false`.
- Existing issue workspace is returned as a workspace ref.

Frontend coverage:

- Kata detail renders `Create workspace` only for an actionable target.
- Existing workspace renders `Open workspace`.
- Ambiguous or unavailable targets render no workspace button.
- Selecting a different task clears stale target state before the next resolver
  response.
- Settings can add, edit, and remove Kata project mappings using configured
  watched repos.

## Rollout

This can ship behind the existing Kata mode. No migration is needed for automatic
mapping. Manual mappings require a config schema addition but can default to an
empty list.

The implementation should update OpenAPI artifacts after adding the endpoint and
settings schema, then regenerate the frontend API types through the existing
`make api-generate` flow.
