# Kata Task Workspace Launch

## Problem

Kata task detail should offer the same workspace affordance that provider issues
have, but only when middleman can map the selected Kata task's project to one
tracked repository unambiguously.

Kata tasks live in external Kata daemons and are not provider issues. A Kata
issue UID is a string owned by Kata, not a GitHub/GitLab/etc. issue number.
The bridge between the two domains must therefore map only the repository that
should back the local worktree; the workspace owner stays the Kata task.
This must not turn Kata into a provider or make middleman config the source of
truth for Kata daemon/project definitions.

The common case should not need manual setup: watched repositories can already
have configured local clone paths, and those clones can carry `.kata.toml`
metadata that names the Kata project. Manual settings exist only for gaps or
ambiguity.

## Goals

- Show a `Create workspace` action on a Kata task detail when the task's project
  resolves to one tracked provider repository.
- Show `Open workspace` instead when a middleman Kata workspace already exists
  for that Kata task UID.
- Hide the action entirely when repository mapping or Kata issue identity is
  missing, ambiguous, or points at an untracked repository.
- Store Kata workspace ownership by string task UID rather than by numeric
  provider item number.
- Prefer automatic project mapping from configured local clones with `.kata.toml`
  over manual setup.
- Add a settings surface for explicit Kata project to repository mappings.
- Keep Kata task data external to Kata daemons and workspace lifecycle data in
  middleman's existing workspace model.

## Non-Goals

- Treating a Kata task as a provider issue or requiring a synced provider issue
  row before a workspace can be created.
- Moving Kata project or task records into middleman's SQLite schema.
- Scanning arbitrary filesystem paths for `.kata.toml`.
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
workspace creation endpoint directly.

This is rejected because provider route construction, default-host handling,
workspace existence checks, schema migration rules, and ambiguity rules already
belong on the server. Duplicating them in Svelte would create another place for
provider identity bugs.

### Manual Mappings Only

Settings could require every Kata project to be mapped explicitly.

This is too much ceremony for the expected setup. The user already has watched
repositories and local clones with `.kata.toml`, so middleman should use those
before asking for manual configuration.

### Extend Existing Provider Issues Directly

Kata tasks could be treated as provider issues and reused through provider issue
components.

This is rejected because Kata is a first-class non-provider mode. Its task data
stays owned by external Kata daemons, and only the repository used to create the
local worktree crosses into provider repository identity.

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

## Workspace Ownership Schema

The current `middleman_workspaces` owner model is numeric because it was built
for provider PRs and issues: `item_type` plus `item_number` identifies the
owning item inside a provider repo. Kata task UIDs are strings, so the workspace
table needs a string owner key before Kata workspaces can be represented
correctly.

Add an `item_key` text column and make it the canonical owner key:

- Existing PR and provider-issue workspaces use decimal `item_number` as
  `item_key`.
- Kata workspaces use `item_type = "kata_task"` and `item_key = issue.uid`.
- `item_number` remains populated for PR/provider issue workspaces and is absent
  or ignored for Kata workspaces.
- The workspace uniqueness constraint uses
  `(platform, platform_host, repo_path_key, item_type, item_key)`.

API responses should expose both fields:

- `item_key` is always present.
- `item_number` is present only for numeric provider-owned workspaces.

Existing PR and issue summary joins continue to use `item_number`. Kata
workspace summaries do not join provider issue or PR tables; they use the stored
Kata task summary fields described below.

## Kata Workspace Metadata

Middleman should not copy full Kata tasks into SQLite, but a workspace row needs
enough owner metadata to render stable labels when the Kata daemon is unavailable
or the workspace list is open:

- Kata daemon ID.
- Kata project UID and project name.
- Kata issue UID.
- Kata short ID or qualified ID.
- Kata task title.

Store this as a small JSON metadata object or explicit nullable columns attached
to the workspace row. The workspace lifecycle remains middleman-owned; the Kata
daemon remains the source of truth for task details, comments, status, labels,
and project data.

Workspace API responses must include this Kata owner metadata for
`item_type = "kata_task"` workspaces. The metadata is not enough to render the
full task pane; it exists so the workspace list, header, and unavailable-daemon
fallback can name the task without treating it as a provider issue.

## API Shape

Add a middleman API for resolving the selected task's workspace target:

`POST /api/v1/kata/workspace-target`

Request body:

- `daemon_id`
- `project_uid`
- `project_name`
- `issue_uid`
- `short_id`
- `qualified_id`
- `title`

Response body:

- `available: false` when no button should render.
- `available: true` with repository identity, Kata task owner key, and
  optional existing `workspace` ref when an action can render.

The endpoint does not call the Kata daemon. It resolves only from the task data
the frontend already has, middleman settings, local clone `.kata.toml` files, and
middleman's configured watched repositories.

Add a middleman API for creating or reusing a Kata-backed workspace:

`POST /api/v1/kata/workspaces`

Request body:

- Repository identity from the resolver response.
- Kata daemon ID.
- Kata project UID and project name.
- Kata issue UID.
- Kata short ID or qualified ID.
- Kata task title.

The server creates a workspace with `item_type = "kata_task"` and
`item_key = issue_uid`, starting from the mapped repository's current
`origin/HEAD`. Branch names should be derived from the Kata short ID or
qualified ID plus a title slug, not from the opaque UID alone.

Provider PR and provider issue workspace endpoints remain unchanged.

## Frontend Behavior

Kata detail loads a workspace target whenever the selected task changes. The
button is rendered in the existing detail action row:

- `Create workspace` when `available` is true and no workspace ref is returned.
- `Open workspace` when an existing workspace ref is returned.
- No button when `available` is false or the target request fails.

Clicking `Create workspace` calls the Kata workspace endpoint with the resolved
repository and selected task identity, then navigates to the created workspace.
Clicking `Open workspace` navigates to `/terminal/{workspace_id}`.

Transient resolver errors should be surfaced as a small request error in the Kata
detail, not as a permanent task property. The button should avoid stale actions:
while a selected-task resolver request is in flight, the previous task's
workspace target is cleared.

Workspace sidebar behavior is item-type specific:

- Provider issue workspaces keep the existing provider issue sidebar pane.
- Provider pull request workspaces keep the existing PR and review sidebar panes.
- Kata task workspaces do not render the provider issue pane. They render a
  `Kata task` sidebar tab whose body is the same
  `KataIssueDetail.svelte` component used by the regular Kata task browser.

The Kata workspace sidebar should use a thin loader/adapter around
`KataIssueDetail.svelte`, not a second task-detail implementation. The adapter
reads the workspace's stored Kata daemon/project/task identity, switches or
scopes Kata API calls to that daemon, fetches the live task detail/events through
the existing Kata task API path, then passes the resulting data and mutation
callbacks to `KataIssueDetail.svelte` exactly as the regular Kata workspace
does. If the daemon is unavailable, the task cannot be found, or the live fetch
fails, the sidebar shows an unavailable state using the stored Kata metadata
instead of falling back to the provider issue UI.

If `KataIssueDetail.svelte` is too tightly coupled to the full Kata browser
layout or store wiring, refactor that existing component boundary before adding
the workspace sidebar. Acceptable refactors include extracting a shared
presentational task-detail component, moving Kata-browser-specific loading into
its current parent, or making layout-sensitive props explicit. The outcome must
still be one shared task-detail implementation used by both the regular Kata
browser and the workspace sidebar, not a forked sidebar-specific copy.

The sidebar tab selector must therefore branch on `item_type = "kata_task"`
before checking numeric issue state. A Kata workspace has a repository backing
its worktree, but that repository identity must not cause the right sidebar to
look up or render a provider issue with the same number or title.

## Error Handling

The resolver intentionally uses absence for non-actionable states:

- No project mapping.
- Ambiguous project mapping.
- Missing Kata issue UID.
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
- Missing Kata issue UID and removed watched repo mappings return
  `available: false`.
- Existing Kata workspace is returned as a workspace ref.
- Workspace DB migration backfills `item_key` for existing PR and issue rows and
  enforces uniqueness on `item_key`.
- Kata workspace creation stores `item_type = "kata_task"` and a string
  `item_key` without requiring a provider issue row.

Frontend coverage:

- Kata detail renders `Create workspace` only for an actionable target.
- Existing workspace renders `Open workspace`.
- Ambiguous or unavailable targets render no workspace button.
- Selecting a different task clears stale target state before the next resolver
  response.
- Settings can add, edit, and remove Kata project mappings using configured
  watched repos.
- A `kata_task` workspace renders the `Kata task` sidebar tab and mounts
  `KataIssueDetail.svelte` with live Kata task contents.
- If the Kata task detail component is refactored for embedding, the regular
  Kata browser and the workspace sidebar both cover the shared detail component
  so behavior does not split.
- A `kata_task` workspace never renders the provider issue sidebar pane, even
  when its backing repository has a provider issue with a similar identifier.
- Unavailable Kata daemon or missing live task data renders a Kata-specific
  unavailable state using stored workspace metadata.

## Rollout

This can ship behind the existing Kata mode. Automatic mapping needs no config
migration. Manual mappings require a config schema addition but can default to an
empty list. Workspaces require a database migration for `item_key` before the
Kata workspace endpoint ships.

The implementation should update OpenAPI artifacts after adding the endpoint and
settings schema, then regenerate the frontend API types through the existing
`make api-generate` flow.
