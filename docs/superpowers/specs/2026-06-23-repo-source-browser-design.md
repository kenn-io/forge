# Repo Source Browser

## Summary

Add a read-only repository source browser that opens from repository summary cards
and from the command palette when the current selection has a repository context.
The view should feel like a sibling of the existing pull request Files view: a
resizable file sidebar, a central source viewer, and a collapsible right rail for
selected-file history.

The source of truth is middleman's local bare clone cache, not provider contents
APIs. Opening the browser ensures the clone exists and fetches once for the
browser session. Branch and tag switching then reads from that fetched clone until
the user manually refreshes.

## Goals

- Let maintainers browse a configured repository's code from the repository
  summary page.
- Reuse the existing file tree, source/diff viewing, Markdown, category, split
  layout, and route-helper patterns as far as their boundaries allow.
- Support branch and tag browsing with deep links to a selected ref, file path,
  and source/preview mode.
- Show useful per-file recency: last-changed metadata in the sidebar, and recent
  commits touching the selected file in a right rail.
- Keep the browser strictly read-only except for fetch/refresh of the clone.

## Non-Goals

- No file editing, checkout, branch creation, commits, deletes, or worktree
  mutation.
- No content search in the first version. The sidebar has path filtering only.
- No blame view, inline commit diffs, compare workflow, or selected-commit patch
  preview.
- No PR or issue detail header entry points in the first version.
- No provider contents API browser.
- No streaming or partial preview for huge files.
- No broad preview surface for image, PDF, notebook, or archive files.

## Entry Points

Repository summary cards get a `View repo` action. Activating it opens the repo
browser for that card's repository on the repository default branch.

The command palette gets a contextual `View repository` command when the current
selection is repository-bound. Valid contexts are:

- selected activity item with repo identity
- selected pull request
- selected issue
- selected workspace worktree or project

If a workspace has no selected worktree or project, or the selected workspace
context is ambiguous, the command is hidden. The command uses the most relevant
ref for its context: pull requests open at the PR head ref when known, workspaces
open at their branch, and issues/activity fall back to the repository default
branch.

Workspace entry remains bare-clone backed. A workspace branch is only used when
it resolves to an allowlisted branch or commit that exists in the fetched bare
clone. Local-only or unpushed workspace commits are not read through this view.
If the workspace branch is not present after the initial fetch, the browser opens
the repository default branch and shows an inline note that the workspace branch
is not available in the fetched clone. This feature does not add a worktree-backed
source browser mode.

## Architecture

The repo browser owns a new route and store. It should not overload the PR diff
store because the state model is different: selected repo, ref, path, view mode,
full tree entries, blob metadata/content, lazy last-changed metadata, selected
file history, and selected history commit detail.

Reuse should happen below that surface:

- Extend or adapt `PierreFileTree.svelte` so a caller can render full repository
  tree entries, not only `DiffFile[]`.
- Reuse the split layout pattern from `DiffFilesLayout.svelte`: left sidebar,
  `SplitResizeHandle`, main content pane, and an optional right rail.
- Reuse source/code viewer primitives where they fit, while keeping patch diff
  rendering out of this view.
- Reuse Markdown rendering utilities and the docs Markdown view behavior for
  Markdown preview.
- Reuse `diff-categories.ts` for file category filters. Counts are file counts,
  not changed-line totals.
- Extend provider-aware route helpers for repo-code API suffixes instead of
  hand-building URLs.

Backend APIs live under provider-aware repo routes and are backed by
`internal/gitclone.Manager` plus read-only Git commands against the bare clone.
They preserve repository identity as `(provider, platform_host, owner, name)`.

The server first resolves that identity through the provider-neutral repository
model. The lookup contract must return the clone URL, default branch, canonical
repo path, platform host, and forge URL builder inputs. GitHub-only URL assembly
does not belong in the repo browser. Nested repo paths, self-hosted platform
hosts, and provider default hosts must follow the existing platform metadata and
route-helper rules.

## Backend Data Flow

Opening the repo browser ensures/fetches the bare clone and returns repo-code
metadata for the requested ref. Fetch happens once on initial browser open.
Branch and tag switches use the fetched clone. Manual refresh fetches again.

The API surface should cover:

- refs: default branch, remote branches, tags, stable ref ids, and resolved SHAs
- tree: all tracked file paths at a selected ref
- last-changed batch metadata: file path to last commit date, author, and short
  SHA, loaded lazily after the tree renders
- blob: selected file metadata and content at a selected ref/path
- file history: recent commits touching the selected file
- commit detail: metadata-only detail for a selected history commit
- Markdown asset/blob read: repo/ref/path-aware content for relative links and
  images rendered by Markdown preview

All file reads are bounded. Large text files return metadata and a clear
too-large state. Binary files return metadata and a binary-file state. The first
version does not stream partial text.

Tree and history operations are also bounded:

- tree responses cap total entries and return a typed truncation state when the
  selected ref exceeds that cap
- the frontend tree must remain virtualized or otherwise bounded so large
  allowed trees do not render every row eagerly
- last-changed metadata is requested in batches for currently visible or nearby
  file rows, with a maximum path count per request
- file history returns a fixed maximum number of commits, newest first
- commit detail is only available for commits returned by file history or for
  allowlisted commit SHAs already resolved by the repo-code API

The implementation plan should choose exact numeric limits before code is
written and pin them in tests.

## Ref And Path Identity

Do not pass raw user-provided revision expressions into Git commands. The client
selects refs from the refs API, and server routes accept an explicit ref identity:

- `ref_type`: `branch`, `tag`, or `commit`
- `ref_name`: the provider/display name for branches and tags
- `ref_sha`: the canonical resolved commit SHA returned by the refs API

Branch and tag names can contain slashes and can share the same display name, so
`ref_type` and `ref_sha` are part of the identity. A path is always an encoded
repository-relative path, never a filesystem path. The server rejects paths that
normalize outside the selected Git tree and rejects ref names or SHAs that were
not returned by the refs/open metadata flow.

Deep links may carry the display ref name for readability, but the server must
resolve it through the allowlisted refs model and return the canonical SHA used
for subsequent tree/blob/history requests.

## UI Layout

The page is a dense maintainer tool surface, not a marketing page.

Header:

- repository identity
- branch/tag selector
- refresh button
- open-on-forge action
- selected path breadcrumbs

Left sidebar:

- resizable and wider than the current diff default
- path filter input
- category toggles for Plans/docs, Code, Tests, Other, Generated, and All
- full tracked file tree at the selected ref, including dotfiles, generated,
  vendored, and config paths
- compact last-changed indicator per file once metadata loads

Main pane:

- read-only source viewer for text/code files
- Markdown source/preview toggle for Markdown files
- Markdown preview resolves relative links and images against the selected
  repository ref
- inline page states for loading, fetch/read errors, binary files, large files,
  and paths missing on the selected ref

Right rail:

- collapsible file history for the selected file
- compact commit list with short SHA, subject, author, relative date, and
  open-on-forge link
- selecting a commit shows a metadata-only detail panel below the list, in the
  same list-row-selects-detail style as Kata and roborev surfaces

History is scoped to the selected ref. For branches and tags, file history walks
backward from the selected resolved SHA. Rename following is deferred unless Git
can provide it cheaply within the fixed history limit; the first version may show
history for the selected path name only. Tag browsing is read-only and uses the
tag's resolved commit SHA as the history root.

## Routing

The repo browser deep link carries:

- provider and platform host when needed
- owner/name or repo path, following existing provider-aware route conventions
- selected ref type, display ref name, and resolved SHA when known
- selected file path
- source/preview mode

When no path is present, the browser auto-selects a README variant if present.
If no README exists, it shows the tree with no selected file.

When switching refs, the browser preserves the selected path if that path exists
on the new ref. If it does not exist, the route keeps the path and the main pane
shows a "file not found on this ref" state with a clear/select-another-file
action.

## Markdown Preview

Markdown preview should reuse the existing Markdown rendering path and safety
helpers rather than adding a second renderer. Repo browsing needs a different
link/image resolver from Docs mode:

- relative links resolve to repo browser routes at the selected ref
- relative images resolve through the repo blob/asset endpoint at the selected
  ref
- absolute external links keep existing safe-link behavior
- unsupported or unsafe asset paths fail closed with an inline broken-asset
  affordance rather than reaching outside the selected Git tree

The asset endpoint must set MIME type from detected content, enforce the same
size and binary caps as blob reads, reject path traversal, reject refs outside
the allowlisted ref model, and define an SVG policy before implementation. Cache
headers should be safe for immutable `ref_sha` reads and conservative for display
ref-name reads that can move after refresh.

Other file-type previews are deferred. The renderer boundary should still make
future image or other preview renderers possible without reshaping the page.

## Error Handling

Clone, fetch, ref, tree, blob, history, and asset failures stay inline in the
repo browser. The view does not navigate back to the repo summary page on error.
Each state should explain what failed and offer retry or refresh when that action
can help.

API errors should use stable error codes/details consistent with
`context/error-handling.md`; the frontend should branch on those codes/details,
not prose.

The "browser session" freshness rule is scoped to a mounted browser instance and
its in-memory store. Reloading the page starts a new browser session and may fetch
again. Multiple tabs do not coordinate freshness; each tab may perform its own
initial fetch. Manual refresh always asks the backend to fetch before rereading
refs/tree/blob data.

## Success Criteria

- Opening from a repo summary card fetches once, resolves the default branch, and
  restores the README when one exists.
- Opening from a selected pull request or workspace uses a contextual ref only
  when that ref resolves in the fetched bare clone; otherwise it falls back to
  default branch with an inline explanation.
- Deep links restore provider identity, ref type/name/SHA, selected path, and
  source/preview mode.
- Branch and tag names with slashes and duplicate display names are
  disambiguated by `ref_type` and resolved SHA.
- Large text, binary, over-cap tree, and unavailable-ref cases return typed API
  states that the UI renders inline.
- Host-prefixed provider routes work for non-default hosts.
- GitHub, GitLab, Forgejo, and Gitea use the same provider-neutral repo lookup
  contract, with provider-specific differences hidden behind platform metadata
  and capability boundaries.
- File history is bounded, scoped to the selected resolved SHA, and never runs an
  unbounded per-file log across the whole tree.

## Testing

Backend tests should use real temporary Git repositories and exercise observable
API behavior through `srv.ServeHTTP` or the generated API client where it fits.
New repo-code routes should include full-stack API plus SQLite coverage so route
resolution, repository lookup, provider identity, and generated client shapes are
tested together.
Cover:

- ensure/fetch behavior
- branch and tag listing
- full tree listing, including dotfiles and noisy tracked paths
- tree truncation and last-changed batch limits
- blob caps and binary detection
- Markdown asset path safety
- file history and commit metadata
- ref/path validation, including duplicate branch/tag names and slash-containing
  refs
- contextual ref fallback inputs
- provider-aware host routing

Frontend tests should primarily use Vitest/component coverage:

- route parsing/building and route helper usage
- repo browser store loading, ref switching, and path preservation
- README auto-selection
- sidebar path filtering and category filtering
- lazy last-changed metadata rendering
- inline error states
- Markdown source/preview toggle and repo-relative link/image resolution
- command palette visibility for repository-bound selections

Use browser-tier tests only where real DOM behavior matters, such as resize or
layout. Reserve Playwright for a full-page smoke or screenshot only if the final
implementation materially changes the visible shell.

## Implementation Notes

Avoid compatibility shims or legacy URL aliases unless explicitly approved. This
is a new surface, so it should use provider-aware routes and typed route helpers
from the start.

The first implementation should keep history detail metadata-only. Do not add an
inline patch viewer inside the right rail until that interaction is designed
separately.

Recommended implementation order:

1. Define repo-code API models, ref identity, caps, stable errors, route helpers,
   and provider-neutral repository lookup.
2. Implement backend refs/tree/blob/history/asset endpoints with full-stack API
   tests and generated client updates.
3. Add the frontend route and store over the generated client shapes.
4. Adapt shared file tree/source viewer boundaries for full repo tree entries and
   read-only blobs.
5. Build the repo browser layout, sidebar filters, README selection, ref switch
   behavior, and inline states.
6. Add Markdown preview asset resolution.
7. Add the right history rail.
8. Add repo card and command palette entry points.
9. Run the affected frontend suite and backend tests after final edits.
