# Issue workspace directory recovery design

Recover an issue workspace whose database row was lost while its expected
Middleman worktree remains on disk.

## Problem

Creating an issue workspace currently stops at a branch conflict when the
requested branch already exists. `Use Existing Branch` works only when Git can
check that branch out into a new worktree. If the branch is already checked out
in the issue's original Middleman worktree, Git rejects the second checkout and
the dialog appears to do nothing.

This commonly occurs after restoring or rolling back Middleman's database: the
workspace row is gone, but the worktree and its branch still exist at the
deterministic issue-workspace path.

## Scope

Add `Use Existing Directory` to the issue branch-conflict dialog. The action
re-registers only the deterministic directory Middleman would use for that
provider issue. It does not accept a user-supplied path, discover arbitrary
worktrees, or turn the workspace API into a generic worktree registry.

`Use Existing Branch` remains separate. It continues to create a new worktree
from an existing branch that is not checked out elsewhere.

## Interaction

The branch-conflict dialog presents three recovery choices:

1. `Use Existing Branch` checks the existing branch out into a new Middleman
   worktree.
2. `Use Existing Directory` re-registers the expected Middleman worktree that
   already checks out the branch.
3. `Create New Branch` creates the worktree under the edited branch name.

The directory action explains that it considers only the expected
Middleman-managed location. The client submits the conflicting branch together
with `reuse_existing_directory: true`.

On success, the existing inline-workspace handoff applies: the confirmed
workspace is recorded, the issue remains selected, and the workspace opens in
the dock. Without an inline controller, the client navigates to the workspace
terminal as it does for ordinary issue-workspace creation.

If recovery is unavailable, the dialog remains open and displays the server's
actionable reason. Repeated branch-conflict responses must never replace the
dialog with an identical error-free state.

## API and manager behavior

The issue-workspace request body gains the optional boolean
`reuse_existing_directory`. The provider-aware default-host and explicit-host
routes expose the same field through the generated OpenAPI clients.

`workspace.CreateIssueOptions` gains the corresponding option. When selected,
the manager constructs the ordinary issue workspace identity and deterministic
worktree path, then validates the existing directory before inserting a
workspace row.

Recovery succeeds only when all of these conditions hold:

- the deterministic worktree path exists and is a directory;
- it is a valid linked Git worktree, not merely a directory containing a
  repository;
- its common Git directory is the expected managed clone or the currently
  configured local worktree base for the same provider, host, owner, and repo;
- Git records the deterministic path as an owned linked worktree; and
- its current branch is the branch submitted with the recovery request.

The existing worktree-provenance and branch-validation machinery remains the
authority for these checks. Recovery does not move the branch, reset `HEAD`,
clean files, remove directories, or alter the worktree before the workspace row
is safely persisted.

After insertion, normal workspace setup adopts the validated worktree, starts a
fresh terminal session, records the actual workspace branch, and marks the
workspace ready. Dirty and untracked files remain untouched. The new database
row and terminal session receive new workspace identities; recovering an old
terminal process is out of scope.

## Errors and atomicity

A missing or invalid expected directory fails synchronously before database
insertion. The server returns a stable problem response that the client can
render inside the conflict dialog. The detail distinguishes at least these
operator-actionable cases:

- the expected directory does not exist;
- the path is not a linked worktree for the repository; or
- the worktree checks out a different branch.

Validation and insertion cannot be perfectly atomic with external filesystem
changes. Setup repeats the existing ownership and branch checks before adopting
the directory. If the worktree changes after preflight, setup records the
workspace as errored without deleting or rewriting the pre-existing directory.
The ordinary workspace recovery/delete controls then govern the persisted row.

## Testing

- Manager tests create an orphaned deterministic issue worktree, recover it,
  and prove its branch, commit, dirty files, and untracked files are preserved.
- Manager tests reject a missing path, wrong repository, non-linked checkout,
  and wrong branch without inserting a workspace row.
- A server test uses the generated client through `ServeHTTP` to prove the new
  request field recreates a ready workspace from the expected directory and
  exposes stable failure responses.
- A component test proves the dialog submits the recovery flag, completes the
  existing inline handoff on success, and keeps the dialog open with an
  actionable error on rejection.
- The existing Playwright test continues to prove successful branch reuse. The
  affected issue-workspace browser workflow is rerun after the final UI edit.

## Non-goals

- Selecting or typing an arbitrary directory.
- Adopting a primary checkout or unrelated worktree.
- Recovering the old workspace ID or terminal session.
- Automatically adopting a directory from `Use Existing Branch`.
- Changing pull-request, Kata, or ad-hoc workspace creation.
