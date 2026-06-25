# Workspace APIs

These APIs manage **middleman-owned workspaces**: durable local execution
contexts for tracked PRs, provider issues, and mapped Kata tasks. They are not a
generic Git worktree browser and not an embedder protocol for arbitrary host
state.

## Purpose

- Persist a middleman workspace entry for a tracked item.
- Materialize that entry as a local Git worktree plus tmux session.
- Let the UI reopen the same workspace from `/workspaces` or `/terminal/:id`.
- Carry enough item metadata to render the correct sidebar behavior.

## Endpoint Intent

- `POST /workspaces`: create or reuse a PR-backed workspace.
- `POST /repos/{owner}/{name}/issues/{number}/workspace`: create or reuse an
  issue-backed workspace; these start from the repo's current `origin/HEAD`,
  not from a PR head branch.
- `POST /kata/workspace-target`: resolve whether a Kata task has a clear
  repository target. The UI should hide the workspace action when this returns
  `available:false`.
- `POST /kata/workspaces`: create or reuse a Kata-task-backed workspace. Kata
  tasks are not provider issues, so this path never resolves or syncs a
  provider issue row.
- `GET /workspaces`: list middleman's persisted workspaces for the workspaces
  page and terminal picker.
- `GET /workspaces/{id}`: load one persisted workspace for terminal view.
- `DELETE /workspaces/{id}`: tear down a middleman-managed workspace and its
  local resources.

## Data Model Intent

- `item_type`: whether the workspace belongs to a `pull_request`, provider
  `issue`, or `kata_task`.
- `item_key`: the canonical owner key within the repo/workspace namespace. PR
  and provider issue workspaces use the decimal item number as a string; Kata
  task workspaces use the Kata issue UID.
- `item_number`: the provider item number within the repo. For Kata task
  workspaces this is `0` and must not be used for owner identity.
- `git_head_ref`: the Git branch name middleman opens in the worktree.
- `item_last_activity_at`: the synced provider item activity timestamp for the
  owning PR or issue, when middleman has that owner item row.

These fields exist so PR-backed workspaces show PR/Reviews sidebars, while
issue-backed workspaces show the issue sidebar and disable the PR/reviews path.
Kata-backed workspaces show an embedded live Kata task pane using the same task
detail component as the Kata browser.

Workspace summaries join the owning PR or issue row by full provider identity:
`platform`, `platform_host`, `repo_owner`, `repo_name`, `item_type`, and
`item_number`. A PR workspace uses `middleman_merge_requests.last_activity_at`;
an issue workspace uses `middleman_issues.last_activity_at`. Kata workspaces do
not join provider item tables and leave provider item activity absent. If the
owning provider item has not synced yet, the summary leaves
`item_last_activity_at` absent rather than inventing a value.

Kata task repository resolution is deliberately exact. Manual settings mappings
key by optional daemon ID plus Kata project UID and point to a configured exact
repo identity. Automatic resolution only uses watched exact repos with
`worktree_base_path` whose clone contains a matching `.kata.toml`; ambiguous or
missing matches mean the Create/Open workspace button must not render.

All workspace API timestamps are emitted as UTC RFC3339 strings. Keep timestamp
normalization in the DB/server boundary; the Svelte UI can present local time
where needed.

## Sidebar Ordering

The workspace sidebar has two separate activity concepts:

- `Activity`: terminal/runtime activity, ordered by `tmux_last_output_at` with
  `created_at` as the fallback.
- `Item activity`: provider item activity, ordered by `item_last_activity_at`
  with `created_at` as the fallback.

Keep these modes distinct. Do not relabel `Activity` to mean provider PR/issue
activity, and do not add compatibility aliases for old sort values without an
explicit migration reason.

`Org / repo` is the grouped ordering mode. Timestamp sorts are flat lists, with
ties broken deterministically by workspace ID so the visible order does not
shift between refreshes.

## Testing Expectations

Workspace API changes that alter summary fields or sorting inputs need coverage
at the boundary a client observes:

- DB summary tests should prove PR-backed, issue-backed, Kata-backed, and
  unsynced-owner workspaces expose the expected `item_last_activity_at` shape.
- Server/API tests should assert `/api/v1/workspaces` returns the generated JSON
  field for synced owner items and omits it for missing owner rows.
- Frontend sidebar tests should cover the relevant sort mode and fallback.
- Visible workspace sidebar changes need affected Playwright coverage before
  pushing.

## Non-Goals

- Represent arbitrary worktrees discovered on a host machine.
- Mirror an external workspace tree or host inventory.
- Serve as a generic Git automation API outside middleman's workspace lifecycle.

## Related context

- [`context/workspace-runtime-lifecycle.md`](./workspace-runtime-lifecycle.md)
  documents runtime-session exit, tmux persistence, and destructive ordering
  rules that sit underneath these APIs.
