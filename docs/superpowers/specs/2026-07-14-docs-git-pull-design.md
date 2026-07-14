# Docs Git Pull

Date: 2026-07-14
Status: approved

## Summary

The docs view can commit and push markdown changes ("publish") but has no way
to bring down remote changes. This adds a pull action: a button next to the
existing publish button in the docs workspace header that fast-forwards the
local branch to its upstream.

## Decisions

- **Fast-forward only.** Pull is `git fetch` + `git merge --ff-only`. When the
  local branch has diverged from upstream, pull fails with a typed error
  telling the user to resolve in a real git client. Middleman never creates
  merge commits, never runs merge drivers on untrusted repo content, and never
  leaves conflict markers in docs files. (User-approved 2026-07-14.)
- **No dialog.** Pull takes no input, so the button acts immediately and
  reports its outcome in the same status line style publish uses.
- **Same visibility gate as publish.** The button renders only when the folder
  is a git repo (`is_repo` from the git status route, including the
  `unsafe_git_config` exception that keeps the action visible so the safety
  explanation can surface).

## Backend: `internal/docs/git_pull.go`

`(*Registry) GitPull(ctx, folderID) (PullResponse, error)` mirrors
`GitPublish`'s safety pipeline, in order:

1. `Lookup` + `isGitRepo` (`ErrNotAGitRepo` when not a repo).
2. `assertSafeToPublish` — the command-bearing-config gate. The existing
   denylist already covers the fetch-relevant keys (`remote.*.uploadpack`,
   `remote.*.vcs`, `core.sshcommand`, `credential.*`).
3. `assertWorktreeAttributesSafe` — must precede any command that rehashes
   worktree content; the ff checkout rewrites files.
4. `currentBranch` + upstream resolution via the same config reads publish
   uses. No upstream → `NoUpstreamError` with a suggested
   `git branch --set-upstream-to=origin/<branch> <branch>` command.
5. **Fetch-target safety**, the fetch-side twin of `assertPushTargetSafe`:
   classify every fetch URL of the upstream remote
   (`git remote get-url --all`, falling back to the raw upstream string) with
   the existing `classifyPushURL` logic. Remote-helper transports and URLs
   resolving inside the docs folder are rejected. Local paths outside the
   folder are allowed with a hardened `--upload-pack` (empty hooksPath,
   `core.fsmonitor=false`, `uploadpack.packobjectshook` cleared), the fetch
   analogue of `localReceivePack`. Mixed local/network URL sets are refused
   for the same per-invocation-hardening reason as push.
6. `git fetch <remote> <mergeRef>` (populates `FETCH_HEAD`).
7. Divergence check via `git merge-base --is-ancestor` (no stderr parsing):
   - `FETCH_HEAD` ancestor of `HEAD` (or equal) → already up to date; return
     `UpToDate: true` without merging.
   - `HEAD` ancestor of `FETCH_HEAD` → fast-forward possible.
   - Neither → `ErrDiverged`.
8. `git merge --ff-only FETCH_HEAD`. A refusal (for example locally modified
   files that the update would overwrite) surfaces as
   `PullFailedError{Stderr}`.

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

`Commit`/`ShortCommit` are the post-pull `HEAD` (unchanged when
`UpToDate`).

Typed errors: `ErrDiverged` (sentinel), `PullFailedError{Stderr}` (fetch or
merge failure), reusing `NoUpstreamError`, `ErrNotAGitRepo`, and
`UnsafeGitConfigError`.

## Server route

`POST /docs/folders/{id}/git/pull` (operation `pull-docs-git`) in
`internal/server/docs_routes.go`. It acquires the same per-folder
`docsPublishLocks` entry publish uses so a pull and a publish (or two pulls)
cannot interleave on one folder. When the lock is held, pull returns a 409
problem with reason `gitOperationInProgress` and message "another git
operation is in flight for this folder"; the publish route keeps its existing
`publishInProgress` reason unchanged.

Error mapping (stable `reason` codes in the problem envelope):

| Error | Status | reason |
|---|---|---|
| `ErrNotAGitRepo` | 400 | `notGitRepo` |
| `NoUpstreamError` | 400 | `noUpstream` (+ branch, suggested_command) |
| `ErrDiverged` | 409 | `diverged` |
| `PullFailedError` | 502 | `pullFailed` |
| `UnsafeGitConfigError` | 400 | `unsafeGitConfig` |

Regenerate API artifacts with `make api-generate`.

## Frontend

- `frontend/src/lib/api/docs/api.ts`: `gitPull(folderID)` posting to the new
  route; response type in `types.ts`.
- `DocsWorkspace.svelte`: a Download-icon `list-action` button rendered
  directly before the publish button under the same `activeFolderIsRepo`
  gate, `title="Pull from git"`, disabled while a pull is in flight.
- On success: status line "Already up to date." or
  "Pulled to <short_commit>."; reload the tree, git status, and the currently
  open document (its content may have changed on disk).
- On failure: surface the API error message in the same status area, keeping
  the `diverged` case's guidance readable.

## Testing

- `internal/docs/git_pull_test.go`: table-driven tests against local fixture
  remotes following the `git_publish` test patterns (up to date, fast-forward,
  diverged, no upstream, not a repo, unsafe config, remote-helper URL
  rejected, in-folder remote rejected, dirty-worktree overwrite refusal).
- `internal/server/docs_git_routes_test.go`: route-level success and error
  mapping through `srv.ServeHTTP`, plus lock contention.
- Frontend Vitest (jsdom): button visibility gate, pull success refreshing
  tree/status/open doc, error surfacing; mock API per testing rules since the
  behavior under test is frontend-owned.

## Out of scope

- Ahead/behind indicators or auto-fetch to hint that a pull is needed.
- Any conflict resolution UI.
- Pull for non-git docs folders (button hidden, route errors).
