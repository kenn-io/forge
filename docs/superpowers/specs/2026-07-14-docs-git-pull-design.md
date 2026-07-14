# Docs Git Pull

Date: 2026-07-14
Status: implemented (revised)

## Summary

The docs view can commit and push markdown changes ("publish") but has no way
to bring down remote changes. This adds a pull action: a button next to the
existing publish button in the docs workspace header that fast-forwards the
local branch to its upstream.

Revision note: the first draft of this spec mirrored publish's full safety
pipeline onto pull (fetch-URL classification, hardened upload-pack,
fetched-tree attribute gates). Review concluded that layer was overwrought
for pulling from the folder's own configured upstream — the same remote
publish already pushes to — and that the underlying git plumbing should come
from kit instead of being hand-rolled per feature. The shipped design below
reflects that.

## Decisions

- **Fast-forward only.** Pull is `git fetch` + `git merge --ff-only`. When the
  local branch has diverged from upstream, pull fails with a typed error
  telling the user to resolve in a real git client. Middleman never creates
  merge commits and never leaves conflict markers in docs files. A dirty
  worktree that the update would overwrite is refused by git itself.
- **Pull runs what the user's own `git pull --ff-only` would run.** No
  fetch-target classification or pull-specific config gates: the upstream is
  the user's own configured remote, trusted exactly as much as publish
  already trusts it as a push destination. The standard docs runner
  hardening (hooks neutralized, fsmonitor off, protocol allowlist, stripped
  env) applies because pull goes through the same plumbing as every other
  docs git command.
- **Docs git plumbing rides on kit.** `internal/docs` uses
  `go.kenn.io/kit/git/cmd` (`gitcmd.Runner`) like the rest of middleman
  (workspace, gitclone, server), instead of its own env stripping and
  command construction. The docs runner differs from `gitcmd.New()` in one
  way: global and system git config stay readable, because docs commits
  need the maintainer's identity, filters, and credential helpers. Two
  docs-specific concerns remain local: middleman/msgvault secret env
  stripping, and the command-scope overrides neutralizing untrusted
  repo-local config.
- **No dialog.** Pull takes no input; the button acts immediately and
  reports its outcome in the same notice style publish uses.
- **Same visibility gate as publish.** The button renders only when the
  folder is a git repo (`is_repo` from the git status route, including the
  `unsafe_git_config` exception that keeps actions visible so the safety
  explanation can surface).

## Backend: `internal/docs/git_pull.go`

`(*Registry) GitPull(ctx, folderID) (PullResponse, error)`:

1. `Lookup` + `isGitRepo` (`ErrNotAGitRepo` when not a repo).
2. `currentBranch`; upstream resolution via the same config reads publish
   uses. No upstream → `NoUpstreamError` suggesting
   `git branch --set-upstream-to=origin/<branch> <branch>`.
3. `git fetch <remote> <mergeRef>` (populates `FETCH_HEAD`). Failure →
   `PullFailedError{Stderr}`.
4. Divergence check via `git merge-base --is-ancestor` exit codes (no
   stderr parsing):
   - `FETCH_HEAD` ancestor of `HEAD` (or equal) → `UpToDate: true`, no merge.
   - `HEAD` ancestor of `FETCH_HEAD` → fast-forward.
   - Neither → `ErrDiverged`.
5. `git merge --ff-only FETCH_HEAD`. A refusal (for example locally
   modified files the update would overwrite) → `PullFailedError{Stderr}`.

Wire shape:

```go
type PullResponse struct {
    Branch      string `json:"branch"`
    Upstream    string `json:"upstream"`
    UpToDate    bool   `json:"up_to_date"`
    Commit      string `json:"commit"`
    ShortCommit string `json:"short_commit"`
}
```

`Commit`/`ShortCommit` are the post-pull `HEAD` (unchanged when `UpToDate`).

Detached HEAD and unborn-branch states need no special casing:
`currentBranch` (`git symbolic-ref --short HEAD`) fails on a detached HEAD,
and an unborn branch has no upstream so it takes the `NoUpstreamError` path.

## Server route

`POST /docs/folders/{id}/git/pull` (operation `pull-docs-git`) in
`internal/server/docs_routes.go`. Pull and publish share the in-flight lock,
keyed by the folder's canonical path rather than its ID: the registry allows
several folder IDs over one path, and a git operation is per-repository
(`FETCH_HEAD`, the index, and `HEAD` are repo-global). When the lock is
held, pull returns 409 with reason `gitOperationInProgress`; the publish
route keeps its existing `publishInProgress` reason.

Error mapping (stable `reason` codes in the problem envelope):

| Error | Status | reason |
|---|---|---|
| `ErrNotAGitRepo` | 400 | `notGitRepo` |
| `NoUpstreamError` | 400 | `noUpstream` (+ branch, suggested_command) |
| `ErrDiverged` | 409 | `diverged` |
| `PullFailedError` | 502 | `pullFailed` |

API artifacts regenerated with `make api-generate`.

## Frontend

- `frontend/src/lib/api/docs/api.ts`: `gitPull(folderID)`; response type in
  `types.ts`; reasons `diverged`/`pullFailed`/`gitOperationInProgress`
  mapped to frontend error codes.
- `DocsWorkspace.svelte`: a Download-icon `list-action` button directly
  before the publish button under the same `activeFolderIsRepo` gate,
  `title="Pull from git"`. Disabled while a pull is in flight or while the
  markdown editor is open — a pull that rewrites the open document would
  recreate the editor and silently discard an unsaved draft.
- On success the tree, git status, and the open document reload (the pulled
  commit may have rewritten the doc on disk; a deleted doc surfaces through
  the existing load-error path), then the shared notice line reports
  "Already up to date." or "Pulled to <short_commit>.". Failures surface
  the API error message in the same notice line.

## Testing

- `internal/docs/git_pull_integration_test.go`: fixture-remote tests
  (fast-forward, up to date, diverged, dirty-worktree overwrite refusal,
  no upstream, not a repo).
- `internal/server/docs_git_routes_test.go`: route-level success and error
  mapping through `srv.ServeHTTP`, lock contention, and two folder IDs
  aliasing one path sharing the lock.
- Frontend Vitest (jsdom): button visibility gate, pull success refreshing
  tree AND git status AND the open document, up-to-date and error notices,
  editor-open disablement.

## Out of scope

- Ahead/behind indicators or auto-fetch to hint that a pull is needed.
- Any conflict resolution UI.
- Pull for non-git docs folders (button hidden, route errors).
- Serializing pull against the docs file-write routes (save/create/rename/
  delete): the ff-only merge refuses to overwrite dirty tracked files, the
  editor-open guard removes the practical draft-clobber race, and the
  equivalent write-vs-git race already exists between publish and saves.
- Moving the untrusted-repo hardening (config denylist, attribute gates,
  push-target classification) into kit: kit-shaped follow-up work, tracked
  separately from this feature.
