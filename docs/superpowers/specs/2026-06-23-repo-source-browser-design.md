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

## Backend Data Flow

Opening the repo browser ensures/fetches the bare clone and returns repo-code
metadata for the requested ref. Fetch happens once on initial browser open.
Branch and tag switches use the fetched clone. Manual refresh fetches again.

The API surface should cover:

- refs: default branch, remote branches, and tags
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

## Routing

The repo browser deep link carries:

- provider and platform host when needed
- owner/name or repo path, following existing provider-aware route conventions
- selected branch/tag/ref
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

## Testing

Backend tests should use real temporary Git repositories and exercise observable
API behavior through `srv.ServeHTTP` or the generated API client where it fits.
Cover:

- ensure/fetch behavior
- branch and tag listing
- full tree listing, including dotfiles and noisy tracked paths
- blob caps and binary detection
- Markdown asset path safety
- file history and commit metadata
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
