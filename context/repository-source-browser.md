# Repository Source Browser

Use this document for repository source-browser routes, bare-clone reads, ref
coherence, file history, previews, or refresh behavior.

## Ownership And Identity

- The source browser is a read-only view over a dedicated local bare clone. It
  does not use provider contents APIs and never reads or mutates a workspace
  worktree.
- Resolve every request through the tracked repository registry using provider,
  platform host, and canonical repository path before accepting its clone URL
  (`internal/server/repobrowserapi/handler.go::Handler.ensureRepoBrowserClone`).
- Clone namespaces include provider, host, and canonical repository path so
  identical owner/name routes cannot collide across providers or hosts
  (`internal/gitclone/repo_browser.go::repoBrowserCloneNamespace`).

## Coherent Reads

- Branches and tags are mutable route inputs. Resolve them once per API request,
  return the resolved SHA and stale-request signal, and pin the tree, blob, or
  history read to that commit (`internal/server/repobrowserapi/handler.go::Handler.resolveRepoBrowserReadRef`).
- A caller-supplied SHA mismatch is observable staleness, not permission to read
  an old target silently (`internal/gitclone/repo_browser.go::Manager.ResolveRepoBrowserRef`).
- Commit detail is valid only when the commit is reachable from the selected
  root ref and changes the selected path; a SHA present elsewhere in the clone
  is out of scope (`internal/gitclone/repo_browser.go::Manager.RepoBrowserCommitDetail`).

## Content Boundary

- Treat paths as repository-relative literals: reject absolute paths, traversal,
  NULs, and magic pathspec interpretation
  (`internal/gitclone/repo_browser.go::cleanRepoBrowserPath`).
- Keep ref, tree, path-batch, blob, log, and history limits explicit. A browser
  request must not turn a large repository into an unbounded response or Git
  walk (`internal/gitclone/repo_browser.go::RepoBrowserTreeEntryLimit`).
- Asset responses require an immutable full commit SHA, use `nosniff`, and allow
  only bounded raster formats. SVG and other active or unsupported content stay
  out of same-origin asset responses
  (`internal/server/repobrowserapi/handler.go::repoBrowserAssetRefIsImmutable`,
  `internal/gitclone/repo_browser.go::repoBrowserAssetMediaTypeAllowed`).

## Refresh Model

- First access ensures the isolated clone exists. Once known, clones refresh on
  the configured sync cadence, and tag fetches remain browser-specific so the
  general clone hot path is not coupled to the tag namespace
  (`internal/server/repobrowserapi/refresh.go::Handler.RunRefreshLoop`,
  `internal/gitclone/repo_browser.go::Manager.fetchRepoBrowserTags`).
- A missing clone may finish its single-flight initial fetch after the opening
  caller cancels; later callers share that bounded work rather than starting
  competing clones (`internal/gitclone/repo_browser.go::Manager.ensureRepoBrowserCloneLocal`).

## Related Context

- [`platform-sync-invariants.md`](./platform-sync-invariants.md): tracked
  repository identity and provider-host routing.
- [`retries-and-backoffs.md`](./retries-and-backoffs.md): bounded network retry
  policy used by clone refreshes.
