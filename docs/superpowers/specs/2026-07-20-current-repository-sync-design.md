# Current Repository Sync

## Purpose

Add a split sync control to the desktop app header. The primary segment keeps
the existing full-sync behavior. The chevron segment exposes a separate action
that syncs only the repository in the current UI context.

## Interaction

The control renders as one 28px button group:

```text
[ sync icon  Sync | chevron ]
```

- Clicking the icon or label triggers the existing full sync without changing
  its request or priority behavior.
- Clicking the chevron opens a compact popover menu with one item: `Sync current
  repo`.
- A route-specific repository wins for pull request, issue, focus, and repository
  source routes. Otherwise, exactly one repository selected in the global repo
  selector supplies the scope.
- The menu item is disabled when neither source identifies one repository, such
  as an all-repository or multi-repository selection on a non-repository route.
- Both segments and the menu action are disabled while a sync is running. The
  primary segment continues to show the existing spinner and `Syncing...` label.
- The chevron exposes `aria-expanded` and `aria-haspopup="menu"`. Escape and an
  outside press close the menu and restore focus to the chevron.

The menu uses the shared popover surface and dismissal behavior so it is not
clipped by the top bar or split-pane layout.

## Repository Identity

Scoped sync requests carry the full provider-qualified identity:

```text
provider|platform_host/repo_path
```

The server resolves this value against configured repositories. Owner, repo, or
number alone must never select the sync target.

## API And Syncer

Extend `POST /sync` with an optional repeated `only_repo` query parameter. It
uses the same provider-qualified syntax as `priority_repo`, but the two options
have different behavior:

- No `only_repo` retains the existing full sync.
- `only_repo` runs the normal repository sync pipeline for only the resolved
  repositories.
- An unresolved or malformed `only_repo` value returns a stable client error and
  must not fall back to a full sync.

The sync store gains a scoped trigger method instead of changing `triggerSync`.
The syncer gains a corresponding entry point that supplies the selected
repository set to the existing bounded, single-flight run machinery. A scoped
sync does not launch the separate notification sync because notifications are
not repository-scoped.

Sync status remains global. Starting either action sets the same running state,
and completion refreshes the same status endpoint.

## Error Handling

Request failures flow through the existing sync-store status field. A rejected
scoped request clears the optimistic running state and records the server error,
matching full-sync failure behavior. The menu closes when the user selects its
enabled action; it does not add a second toast or error surface.

## Verification

- A syncer test proves a scoped trigger visits only the requested repository.
- A server test proves `only_repo` resolves provider, host, and nested repo path,
  and that invalid scope cannot trigger a full sync.
- A sync-store test proves full sync remains unchanged and scoped sync sends
  `only_repo`.
- AppHeader component tests prove the split interaction, route-first resolution,
  single-selector fallback, ambiguous disabled state, dismissal behavior, and
  loading state.
- Svelte analysis, the full frontend test suite, and the affected Go packages run
  after the final edits.
