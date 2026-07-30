# Docs Mode

Use this document for changes to configured markdown folders, Docs HTTP access,
filesystem operations, search, and git pull/publish behavior.

## Ownership And Configuration

- Docs is a non-provider mode. Files remain on disk under explicitly configured
  folder roots; middleman stores folder registration, not document content.
- Folder IDs are stable URL-segment identities. Paths are normalized to absolute,
  symlink-resolved roots before entering the live registry
  (`internal/docs/folder.go::NewRegistry`).
- Folder mutations serialize with config reload and roll the live registry back
  if the whole-file config save fails; config and runtime registry must never
  expose different folder sets
  (`internal/server/docsapi/routes.go::Handler.createDocsFolder`).
- A folder may pin a Kata daemon for task-reference navigation. That binding
  applies only while the daemon remains configured and never falls back to an
  ambient selection
  (`frontend/src/lib/components/docs/folderDaemon.ts::effectiveDocsFolderDaemon`).

## Filesystem And HTTP Boundary

- Every Docs API read, browse, mutation, and git operation requires a loopback
  client even when the daemon is configured with a wider listener
  (`internal/server/server.go::Server.isDocsReadAPIRequest`).
- The registry accepts markdown files and an allowlist of raster image blobs,
  not arbitrary files. SVG stays excluded because same-origin SVG can execute
  script (`internal/docs/folder.go::imageExts`).
- Resolve and contain every requested path after symlink evaluation. Reads,
  writes, renames, deletes, search, and blob serving must not escape the
  registered root or traverse ignored content
  (`internal/docs/folder.go::Registry.resolve`).
- File writes are atomic and in-process mutations are serialized across folders;
  create and rename never overwrite an existing destination
  (`internal/docs/folder.go::Registry.WriteFile`).

## Git Pull And Publish

- Every Docs git command strips inherited credential-like environment
  variables, disables hooks and fsmonitor, and restricts transport helpers
  (`internal/docs/git.go::docsGitRunner`).
- Status rejects command-bearing worktree attributes; changes and publish also
  reject command-bearing local config before inspecting or staging content
  (`internal/docs/git.go::Registry.GitStatus`, `internal/docs/git_publish.go::Registry.GitChanges`).
- Publish handles markdown changes only. Unrelated staged files, partial stages,
  conflicts, missing upstreams, and diverged branches block the operation rather
  than being folded into a Docs commit
  (`internal/docs/git_publish.go::Registry.GitPublish`).
- Push the branch's configured upstream; repository-wide `push.default` or
  `remote.pushDefault` must not redirect publication
  (`internal/docs/git_publish.go::Registry.currentUpstreamPushTarget`).
- Pull is an explicit trusted action: it skips config and attribute gates and
  permits unrelated dirty changes, but rejects divergence and changes Git would
  overwrite during its fast-forward checkout
  (`internal/docs/git_pull.go::Registry.GitPull`).

## Related Context

- [`config-persistence.md`](./config-persistence.md): whole-file save and reload
  invariants.
- [`error-handling.md`](./error-handling.md): typed problem responses and
  frontend recovery.
- [`ui-interaction-contracts.md`](./ui-interaction-contracts.md): folder route
  normalization and cross-mode task navigation.
