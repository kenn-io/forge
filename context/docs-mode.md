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

- Docs git operations treat registered repositories as user data, not trusted
  code: strip inherited credential-like environment variables, disable
  repository hooks and fsmonitor, and refuse command-bearing local config or
  attributes before commands that inspect or stage worktree content
  (`internal/docs/git.go::docsGitRunner`,
  `internal/docs/git_safety.go::Registry.assertSafeToPublish`).
- Publish handles markdown changes only. Unrelated staged files, partial stages,
  conflicts, missing upstreams, and diverged branches block the operation rather
  than being folded into a Docs commit
  (`internal/docs/git_publish.go::Registry.GitPublish`).
- Push the branch's configured upstream; repository-wide `push.default` or
  `remote.pushDefault` must not redirect publication
  (`internal/docs/git_publish.go::Registry.currentUpstreamPushTarget`).
- Pull is fast-forward-only and refuses a dirty worktree or diverged history;
  conflict resolution belongs in a git client
  (`internal/docs/git_pull.go::Registry.GitPull`).

## Related Context

- [`config-persistence.md`](./config-persistence.md): whole-file save and reload
  invariants.
- [`error-handling.md`](./error-handling.md): typed problem responses and
  frontend recovery.
- [`ui-interaction-contracts.md`](./ui-interaction-contracts.md): folder route
  normalization and cross-mode task navigation.
