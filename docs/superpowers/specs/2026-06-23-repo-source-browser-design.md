# Repo Source Browser

## Summary

Add a read-only repository source browser that opens from repository summary cards
and from the command palette when the current selection has a repository context.
The view should feel like a sibling of the existing pull request Files view: a
resizable file sidebar, a central source viewer, and a collapsible right rail for
selected-file history.

The source of truth is middleman's shared local bare clone cache, not provider
contents APIs. Opening the browser ensures the clone exists and fetches before
the first read. Branch and tag reads resolve against the current shared clone
state for each request, so another fetch can move those refs without the current
browser tab pressing refresh; responses expose the resolved SHA and stale-token
metadata so the UI can show that movement explicitly.

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
ref for its context. Pull requests and merge requests open at a head branch only
when the head is in the same repository and that branch resolves in the fetched
bare clone. Fork heads and provider-specific synthetic PR/MR refs are deferred
from v1; those contexts fall back to the repository default branch with an inline
note. Workspaces open at their branch only when that branch resolves in the
fetched bare clone. Issues and activity fall back to the repository default
branch.

Workspace entry remains bare-clone backed. A workspace branch is only used when
it resolves to a branch or reachable commit that exists in the fetched bare
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
They preserve repository identity as `(provider, platform_host, repo_path)`,
with owner/name retained only as display and compatibility inputs already
present on repository records.

The server first resolves that identity through the provider-neutral repository
model. The lookup contract must return the clone URL, default branch, canonical
repo path, platform host, owner/name display values, and forge URL builder
inputs. GitHub-only URL assembly does not belong in the repo browser. Nested repo
paths, self-hosted platform hosts, and provider default hosts must follow the
existing platform metadata and route-helper rules.

Default-host routes must canonicalize the omitted `platform_host` to the
provider default before repository lookup, clone/fetch orchestration, response
metadata, and gitclone identity construction. The host-prefixed route for that
same default host must produce the same canonical repository identity.

`internal/gitclone` must expose one shared clone identity helper used by clone
path construction, fetch singleflight keys, fetch operations, and repo-browser
read operations. That helper takes `(provider, canonical_platform_host,
repo_path)`, rejects empty or unsafe components, and encodes slash-containing
`repo_path` as a single repository identity component rather than deriving
owner/name from it. Clone remote URLs and token/auth lookup come only from the
repository record returned by provider-aware lookup; they must not be inferred
from route owner/name placeholders or from the encoded clone path.

## Backend Data Flow

Opening the repo browser ensures/fetches the bare clone and returns repo-code
metadata for the requested ref. Manual refresh fetches branches and tags again
before rereading refs, but repo-browser request/refresh hot paths must not prune
tags from the middleman-owned clone. Deleted remote tags may remain visible until
a separate cache maintenance or repair path handles cleanup. Other reads use the
shared clone as it exists at request time; the backend must not promise per-tab
ref pinning for branch or tag display refs.

The API surface should cover:

- refs: default branch, remote branches, tags, stable ref ids, and resolved SHAs
- tree: tracked file paths at a selected ref, capped with a typed truncation
  state
- last-changed batch metadata: file path to last commit date, author, and short
  SHA, loaded lazily after the tree renders
- blob: selected file metadata and content at a selected ref/path
- file history: recent commits touching the selected file
- commit detail: metadata-only detail for a selected history commit
- Markdown asset metadata: repo/ref/path-aware JSON preflight for images rendered
  by Markdown preview
- Markdown asset bytes: repo/ref/path-aware byte reads for assets whose metadata
  says they are safe and renderable

All file reads are bounded. Large text files return metadata and a clear
too-large state. Binary files return metadata and a binary-file state. The first
version does not stream partial text.

Tree and history operations are also bounded:

- tree responses cap total entries and return a typed truncation state when the
  selected ref exceeds that cap
- direct blob reads for an explicit path still work when the tree is truncated;
  truncation disables full-tree-only sidebar affordances but does not make deep
  links unreadable
- README auto-selection uses a separate bounded README candidate probe, so a
  capped tree does not by itself prevent README restoration
- the frontend tree must remain virtualized or otherwise bounded so large
  allowed trees do not render every row eagerly
- last-changed metadata is requested in batches for currently visible or nearby
  file rows, with a maximum path count per request
- last-changed batches must use one bounded Git history walk per batch, not one
  `git log` process per path
- file history returns a fixed maximum number of commits, newest first
- commit detail is available when the request includes the selected root ref and
  enough path/history context for the server to recompute that the commit is in
  the bounded file-history result

The implementation plan should choose exact numeric limits before code is
written and pin them in tests.

## Ref And Path Identity

Do not pass raw user-provided revision expressions into Git commands. Server
routes accept an explicit ref identity:

- `ref_type`: `branch`, `tag`, or `commit`
- `ref_name`: the provider/display name for branches and tags
- `ref_sha`: the canonical resolved commit SHA returned by the refs API

Branch and tag names can contain slashes and can share the same display name, so
`ref_type` is part of the identity. Branch and tag requests resolve `ref_name`
fresh for each request. If a branch/tag request also supplies `ref_sha`, that SHA
is a staleness token only: the server does not pin to it, and every successful
branch/tag JSON response shape (refs/open metadata, tree, blob, history, commit
detail, and asset metadata) must include `stale: true`, the supplied SHA, and the
current resolved SHA when they differ. Immutable views use `ref_type=commit` with
a full 40-character `ref_sha` and no `ref_name`.

Every successful JSON repo browser response includes a reusable ref object:
`ref: { type, name?, resolvedSha, requestedSha?, stale }`. Branch/tag responses
set `name`, `resolvedSha`, and `stale`; they set `requestedSha` when the request
included a `ref_sha` token. Non-stale responses use `stale: false` and still
include `resolvedSha` so the store never has to infer which SHA backed the read.
Commit responses use `type: "commit"`, `resolvedSha`, and `stale: false`.
Successful `asset-bytes` responses are raw bytes and do not include the JSON ref
object; their generated URLs are commit-pinned with `ref_type=commit` and the
metadata response's `resolvedSha`.

A path is always an encoded repository-relative path, never a filesystem path.
The server rejects NUL bytes, absolute paths, empty path segments that normalize
outside the selected Git tree, and ambiguous raw revision expressions. Git
commands must resolve refs to object IDs before path reads, use
`--end-of-options` where available, and put `--` before path arguments.

Validation must be stateless and derivable from the request plus the current
clone. The server may not depend on a hidden browser-session allowlist. It
resolves branch/tag refs by type/name, verifies commit SHAs are reachable from a
selected root ref or recomputable bounded history context, and returns the
canonical SHA used for the current tree/blob/history read.

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
- `repo_path` as the canonical repository identity
- selected ref type, display ref name, and resolved SHA when known
- selected path, which may be a file or directory
- source/preview mode

The app route is query-based to avoid collisions between slash-containing repo
paths, branch names, and file paths:

`/repo/browser?provider={provider}&platform_host={host}&repo_path={repo_path}&ref_type={branch|tag|commit}&ref_name={name}&ref_sha={sha}&path={path}&view={source|preview}`

The backend API uses provider-aware repo routes with `repo_path` as a required
query parameter for repo-browser endpoints. Default-host routes use
`/repo/{provider}/{owner}/{name}/browser/{operation}` and custom-host routes use
`/host/{platform_host}/repo/{provider}/{owner}/{name}/browser/{operation}`.
Handlers must look up the repository by `(provider, platform_host, repo_path)`;
owner/name route placeholders are display hints derived from the repository
record's stored display owner and display name. For nested providers, the
authoritative `repo_path` may be `group/subgroup/repo` while owner/name remain
the display values used by existing provider route helpers; handlers must never
derive identity, cache keys, or clone paths from owner/name alone. Test coverage
must include a nested repo path where owner/name alone is insufficient.

When no path is present, the browser auto-selects a README variant if present.
If no README exists, it shows the tree with no selected file.

Directory paths are valid selection state for navigation and deep links. A
directory may be an explicit tree entry or an implicit parent synthesized from
tracked file paths; selecting it keeps the tree row selected and shows an empty
main pane rather than requesting blob/history data. Missing-path errors apply
only when the selected path is neither a file nor a directory in the loaded tree
(`packages/ui/src/stores/repo-browser.svelte::repoBrowserPathKind`,
`packages/ui/src/stores/repo-browser.svelte.test.ts`).

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

Markdown image rendering uses two explicit backend operations:

- `asset-metadata` returns JSON with `state`, detected `mediaType`, `size`,
  normalized path, and the reusable ref metadata object. Renderable raster image
  states include the corresponding `asset-bytes` URL pinned to
  `ref_type=commit&ref_sha={resolvedSha}`. Non-renderable states do not include a
  byte URL.
- `asset-bytes` returns bytes only for metadata-approved renderable assets. It
  sets the detected image MIME type, `X-Content-Type-Options: nosniff`, and the
  immutable cache policy because generated byte URLs use the resolved commit SHA.
  Successful byte requests must use `ref_type=commit` with a full resolved SHA;
  direct branch/tag byte requests return `400 validationError` with
  `details.reason = "mutable_ref_not_allowed"` and `Cache-Control: no-store`.
  If called directly for an unsupported, oversized, unsafe, missing, or SVG
  asset, it returns a problem envelope and never returns renderable bytes.

Both operations enforce the same size and binary caps as blob reads, reject path
traversal, and use the same stateless ref validation as blob reads. SVG assets
are not rendered as preview images in v1, even when requested through Markdown,
because opening same-origin repository SVG directly would make untrusted
repository content an app-origin script surface. SVG metadata requests return
`state: "unsupportedAsset"`, `reason: "svg"`, the resolved ref metadata, and no
byte URL. SVG byte requests return `415 badRequest` with
`details.reason = "svg"` and no SVG bytes. Markdown preview must call
`asset-metadata` before emitting an `<img>` URL so unsupported assets can be
replaced with the inline broken-asset affordance instead of relying on a direct
image request to carry JSON state. Metadata cache headers are immutable for
`ref_type=commit` reads and conservative (`no-store` or revalidate-on-use) for
branch/tag display refs that can move between requests.

Direct `asset-bytes` failures use this exhaustive problem-envelope matrix:

- unsafe path: `400 validationError`, `details.reason = "unsafe_path"`
- missing path: `404 notFound`, `details.reason = "missing_path"`
- unavailable ref: `404 notFound`, `details.reason = "unavailable_ref"`
- mutable branch/tag ref: `400 validationError`,
  `details.reason = "mutable_ref_not_allowed"`
- oversized asset: `400 validationError`, `details.reason = "oversized_asset"`
- unsupported non-SVG media type: `415 badRequest`,
  `details.reason = "unsupported_asset"`
- SVG asset: `415 badRequest`, `details.reason = "svg"`

Server tests must assert the matrix for unsafe traversal, missing paths,
unavailable refs, mutable branch/tag refs, oversized assets, unsupported media,
and SVG. These direct byte failure responses use `Cache-Control: no-store`
because they are recovery/error states, not immutable asset bytes.

Other file-type previews are deferred. The renderer boundary should still make
future image or other preview renderers possible without reshaping the page.

## Error Handling

Clone, fetch, ref, tree, blob, history, and asset failures stay inline in the
repo browser. The view does not navigate back to the repo summary page on error.
Each state should explain what failed and offer retry or refresh when that action
can help.

API errors should use stable camelCase problem codes/details consistent with
`context/error-handling.md`; the frontend should branch on those codes/details,
not prose. Do not add repo-browser-specific problem codes for normal readable
states when the request succeeds.

The first version uses existing problem codes for failures:

- `serviceUnavailable` with `details.reason = "clone_unavailable"` when the
  local clone cannot be created or fetched
- `validationError` with `details.field` for malformed ref or path inputs
- `notFound` or `repoNotFound` when the repository, ref, commit, or path cannot
  be found; include `details.reason` such as `unavailable_ref` or `missing_path`
- `payloadTooLarge` only for request payload limits, not for readable repository
  blob states
- `internalError` for unexpected local Git or filesystem failures

Tree truncation, stale branch/tag tokens, binary blobs, oversized blob metadata,
oversized asset metadata, and unsupported SVG asset metadata are modeled as
successful response state enums on metadata/blob endpoints. Direct byte endpoints
serve only renderable bytes; non-renderable byte requests use the
problem-envelope table above.
Missing refs, commits, and paths are non-2xx problem envelopes; the repo browser
renders those failures inline instead of navigating away.

There is no per-browser-session freshness guarantee for branch or tag reads.
Reloading the page starts a new frontend store, but all tabs use the same shared
clone cache and refs can move after any fetch. Manual refresh asks the backend to
fetch before rereading refs/tree/blob data; non-refresh reads report the current
resolved SHA and stale-token state for the shared clone snapshot they actually
used.

## Success Criteria

- Opening from a repo summary card ensures/fetches the shared clone, resolves
  the default branch, and restores the README when one exists.
- Opening from a selected pull request, merge request, or workspace uses a
  contextual branch only when that branch resolves in the fetched bare clone;
  otherwise it falls back to default branch with an inline explanation.
- Deep links restore provider identity, `repo_path`, ref type/name/SHA, selected
  path, and source/preview mode.
- Branch and tag names with slashes and duplicate display names are
  disambiguated by `ref_type` and resolved SHA.
- Large text, binary, over-cap tree, stale-token, and unsupported SVG asset
  cases return typed successful API states that the UI renders inline.
- Unavailable ref, missing commit, and missing path problem envelopes are
  rendered inline by the repo browser rather than navigating away.
- Host-prefixed provider routes work for non-default hosts.
- GitHub, GitLab, Forgejo, and Gitea use the same provider-neutral repo lookup
  contract, with provider-specific differences hidden behind platform metadata
  and capability boundaries.
- File history is bounded, scoped to the selected resolved SHA, and never runs an
  unbounded per-file log across the whole tree.
- Repo tree category filters are path-only in v1. The Generated category reuses
  existing path heuristics from `diff-categories.ts`; Linguist or
  `git check-attr` generated metadata is deferred.

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
- direct blob and README restoration when the tree is truncated
- blob caps and binary detection
- Markdown asset path safety
- file history and commit metadata
- stale branch/tag `ref_sha` response-state semantics on every successful
  branch/tag endpoint response shape
- ref/path validation, including duplicate branch/tag names and slash-containing
  refs
- contextual ref fallback inputs
- provider-aware host routing
- nested `repo_path` repository lookup

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
layout. The final stack must include one full-stack browser or e2e-full smoke
that seeds repository data and verifies navigation into the repo browser, tree
rendering, blob rendering, ref switching, and Markdown preview against the real
HTTP API.

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
