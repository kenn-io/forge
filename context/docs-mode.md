# Docs Mode

Use this document for changes to configured markdown folders, Docs HTTP access,
filesystem operations, search, and git pull/publish behavior.

## Ownership And Configuration

- Docs is a non-provider mode. Files remain on disk under explicitly configured
  folder roots; kenn-forge stores folder registration, not document content.
- Folder IDs are stable URL-segment identities. Paths are normalized to
  absolute, symlink-resolved roots before entering the live registry.
- Config and the runtime folder registry must never expose different folder
  sets: folder mutations serialize with config reload and roll the registry back
  if the config save fails (`internal/server/docsapi/routes.go::Handler.createDocsFolder`).
- A folder may pin a Kata daemon for task-reference navigation. The binding
  applies only while that daemon remains configured and never falls back to an
  ambient selection.
- Route changes cancel obsolete Docs presentation work but never accepted
  reconciliation; accepted mutations serialize application-wide
  (`frontend/src/lib/stores/docs-workflow.ts::DocsWorkflow`).
- Reconcile an ambiguous file or folder mutation with an authoritative read:
  matching state is recovered success, contradictory state keeps the failure,
  and an inconclusive read blocks unsafe retry.
- A confirmed pull stays successful when a follow-up tree, git-status, or
  document read fails; apply what succeeded and report the refresh degradation.

## Filesystem And HTTP Boundary

- Docs browse, reads, mutations, and git operations require a loopback client
  (`internal/server/server.go::Server.ServeHTTP`).
- The registry accepts markdown files and an allowlist of raster image blobs,
  not arbitrary files. SVG stays excluded because same-origin SVG can execute
  script.
- Resolve and contain every requested path after symlink evaluation. Reads,
  writes, renames, deletes, search, and blob serving must not escape the
  registered root or traverse ignored content (`internal/docs/folder.go::Registry.resolve`).
- Writes are atomic, and create and rename never overwrite a destination.
- Docs mutations keep origin/CSRF checks, and file writes stay JSON-wrapped,
  not raw markdown.
- Blob responses stay binary rather than being modeled as JSON.

## Git Pull And Publish

- Every Docs git command strips inherited credential-like environment
  variables, disables hooks and fsmonitor, and restricts transport helpers
  (`internal/docs/git.go::docsGitRunner`).
- Status rejects command-bearing worktree attributes; changes and publish also
  reject command-bearing local config before inspecting or staging content.
- Publish stages markdown changes only. Unrelated or partial stages, conflicts,
  and missing upstreams block before commit; a rejected push can leave a local
  commit for recovery.
- Push the branch's configured upstream; repository-wide `push.default` or
  `remote.pushDefault` must not redirect publication.
- Pull is an explicit trusted action: it skips config and attribute gates and
  permits unrelated dirty changes, but rejects divergence and changes Git would
  overwrite during its fast-forward checkout.

## Related Context

- [`config-persistence.md`](./config-persistence.md): whole-file save and reload
  invariants.
- [`error-handling.md`](./error-handling.md): typed problem responses and
  frontend recovery.
- [`ui-interaction-contracts.md`](./ui-interaction-contracts.md): folder route
  normalization and external Kata task launches.
- [`kata-mode.md`](./kata-mode.md): pinned-daemon reference resolution.
