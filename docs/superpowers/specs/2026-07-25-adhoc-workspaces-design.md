# Ad-hoc workspaces design

Start new work in a middleman workspace without a pre-existing pull request,
provider issue, or Kata task.

## Problem

Every workspace-creation path today requires a source item: `Manager.Create`
needs a synced merge request, `Manager.CreateIssue` needs a synced issue, and
`Manager.CreateKataTask` needs Kata task metadata. A maintainer who wants to
start unplanned work in a tracked repo has to invent an issue first, or create
the worktree by hand outside middleman.

## Concept

An **ad-hoc workspace** is a middleman workspace whose only identity is its
branch. It is created directly against a tracked repository quad
`(provider, platform_host, owner, name)`, starts from that repo's current
`origin/HEAD`, and has no provider item, no item number, and no Kata task.

Non-goals: this is not a generic worktree browser, does not accept an arbitrary
base ref, and stores no task title or description — the branch name is the only
user-supplied label.

`origin/HEAD` is the base in every case, including `reuse_existing_branch`.
Reuse adopts a branch the user already has instead of creating one, but setup
still refuses it unless that branch already points at `origin/HEAD`
(`preferred branch %q points at %s, not %s`) and is not checked out in another
worktree. So reuse changes who created the branch, never where work starts.

## Storage

No migration. `middleman_workspaces.item_type` is unconstrained text and
workspace uniqueness is already
`(platform, platform_host, repo_path_key, item_type, item_key)`.

- `db.WorkspaceItemTypeAdHoc = "adhoc"`.
- `item_number = 0`.
- `item_key = "adhoc:" + <requested branch>` — the branch is the workspace
  identity, so two ad-hoc workspaces for the same branch in the same repo
  collide on the existing unique index and the second create reuses the first.
  The key is the creation-time branch and is never rewritten. Renaming the
  branch inside the worktree is a shell action middleman does not observe, so
  after a rename: a create for the old name still returns this workspace, and a
  create for the new name hits the ordinary local-branch conflict (`409` with a
  suggested alternative) because the renamed branch now exists locally —
  `reuse_existing_branch` does not help either, since setup refuses a branch
  checked out in another worktree. A renamed branch therefore stays bound to its
  original workspace until that workspace is deleted or the branch is renamed
  back; there is no second worktree for the same branch and no reconciliation
  pass, which would require watching every worktree's HEAD.
- `workspaceItemKeyForInsert` and `scanWorkspace` must require an explicit
  `item_key` for ad-hoc rows, exactly as they do for Kata rows. Falling back to
  `strconv.Itoa(item_number)` would give every ad-hoc workspace in a repo the
  key `"0"`.

## Manager

`Manager.CreateAdHoc(ctx, provider, platformHost, owner, name, CreateAdHocOptions)`:

- Repository must already be tracked; otherwise `ErrWorkspaceNotFound` wrapping
  `repository not tracked`.
- Branch: trimmed caller input, or generated `middleman/work-<8 hex>` from the
  new workspace ID when empty.
- Branch name validated through the existing `validateLocalBranchName`.
- Existing local branch handling reuses the issue-workspace helpers:
  `ReuseExistingBranch` reuses an existing local branch, otherwise a typed
  branch-conflict error carries the conflicting branch and a suggested
  alternative. The error type is renamed from
  `IssueWorkspaceBranchConflictError` to `WorkspaceBranchConflictError`
  because both flows now raise it.
- Worktree path: `<worktreeDir>/<platform>/<host>/<owner>/<name>/work-<slug>-<hash8>`,
  where `slug` is the slugified branch and `hash8` is the first 8 hex of
  `sha256(branch)`. Slug alone is not injective across branch names; the hash
  keeps distinct branches on distinct paths, mirroring the Kata scope hash.
- `workspaceUsesOriginHead` gains the ad-hoc type, so setup fetches and creates
  the worktree with `git worktree add -b <branch> origin/HEAD`, the same path
  issue and Kata workspaces already use. No upstream is configured until the
  branch is pushed, which matches issue-backed behavior.

## HTTP API

- `POST /repo/{provider}/{owner}/{name}/workspaces`
- `POST /host/{platform_host}/repo/{provider}/{owner}/{name}/workspaces`

Body: `{ "branch": "...", "reuse_existing_branch": false }`, both optional.
Response: `202` with the standard workspace response body, so the client can
navigate to `/terminal/{id}` while setup runs in the background.

Failure envelopes:

- untracked repo → `404`
- invalid branch name → `400` `validationError` on `body.branch`, the status
  `httpapi.Validation` already emits for every other input check
- local branch conflict → `409` `branchConflict` with `details.branch` and
  `details.suggestedBranch`, plus the
  `urn:middleman:error:workspace-branch-conflict` type. Clients branch on the
  code and read `details`; the envelope carries no per-field `errors[]` copy of
  the same values
- existing ad-hoc workspace for the same branch → `202` with that workspace,
  whatever its status. A `creating` or `ready` record is the workspace the user
  asked for, and a failed one is recovered from its own terminal view (retry
  setup or delete) rather than by re-POSTing, which is how PR- and issue-backed
  dedupe already behaves

Domain failures are mapped from sentinel errors with `errors.Is`
(`ErrWorkspaceNotFound`, `ErrInvalidBranchName`, `ErrWorkspaceDuplicate`), not
from manager message text, so rewording an error cannot turn a `404` into a
`500`.

`refreshWorkspace`'s item-type switch gets an ad-hoc case that refreshes only
the mapped repository index, like the Kata case.

## Generated agent context

`AgentSourceKindAdHoc = "ad-hoc work"`. `AgentContext` gains the checked-out
`WorkspaceBranch`. For an ad-hoc workspace the rendered `CLAUDE.local.md` /
`AGENTS.override.md` states the source kind, the working branch, and that there
is no linked pull request, issue, or task and no configured push upstream, so
an agent does not go looking for item metadata that does not exist. Ownership,
marker, and untrusted-text rules are unchanged.

## Frontend

- `providerRepoPath` gains a `/workspaces` suffix; no hand-built URLs.
- `newWorkspaceDialog` store holds open state plus an optional preselected repo
  so both entry points drive one dialog.
- `NewWorkspaceDialog.svelte` is mounted once in `App.svelte` beside the
  palette, so the command works from any page. It offers a tracked-repo picker
  (from `/repos`), a branch field whose empty state reads
  "auto-generated", surfaces conflict/validation errors inline with the
  suggested branch as a one-click fix, and on success navigates to
  `/terminal/{id}`.
- Workspaces sidebar header gets a "New workspace" button next to the count.
- Command palette gains a global `workspace.new` action, "New workspace".
- The `item_type` unions in `WorkspaceListSidebar`, `WorkspaceTerminalView`,
  `WorkspaceRightSidebar`, and `workspace-inline` accept `"adhoc"`. Ad-hoc rows
  show no item bubble (there is no `#number` and no provider URL), label
  themselves with the branch, and stay searchable by branch and by `work`.

## Testing

- `internal/workspace`: branch generation, explicit branch, conflict error,
  worktree path distinctness, `origin/HEAD` start ref, ad-hoc agent-context
  rendering.
- `internal/db`: ad-hoc `item_key` requirement and per-branch uniqueness.
- `internal/server`: wire-level create through the generated client — success,
  reuse, untracked repo, invalid branch, conflict envelope, and old-name vs
  new-name creates after an in-worktree branch rename.
- Vitest: dialog behavior (auto branch vs explicit, error surface, navigation),
  sidebar button opens the dialog, palette action visibility, sidebar rendering
  of an ad-hoc row.
- Playwright only if the sidebar change needs visual proof; the dialog is
  component-owned behavior.
