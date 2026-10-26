# Semantic Search

Use this document when changing the embedding index, its configuration, or
the semantic search API and MCP tool.

## Ownership

- `[embeddings]` is kit's `embedconfig.Embedder`; the zero value disables
  semantic search and is omitted on save. Put Forge-specific settings beside
  it, never inside it (`internal/config/config.go::Config`).
- `[embeddings]` is startup-bound: the encoder and its API key are resolved once
  (`internal/server/configreload/config_reload.go::StartupConfigSnapshot`).
- Only the provider owner builds an index; spokes forward the `semantic-search`
  route to the hub and never index locally (`cmd/kenn-forge/main.go`).

## What is embedded

- PR and issue title plus body, and comment, review, and review-comment bodies
  from active repositories, never code, diffs, or commits
  (`internal/db/queries_semantic.go`).
- Comment sources must include the `comment` event type: Bitbucket persists
  conversation comments under it, not `issue_comment`
  (`internal/db/queries_semantic.go`).
- A comment embeds its body alone, so renaming an item does not re-embed its
  comments (`internal/semantic/mirror.go::documentContent`).
- Bump `recipe` whenever the embedded text or chunk window changes, or stored
  vectors silently stop matching new ones (`internal/semantic/encoder.go::recipe`).

## Index

- The index is a rebuildable sidecar, `semantic.db` under `data_dir`; a schema
  mismatch deletes it rather than migrating. Keep its tables out of the Forge
  database (`internal/semantic/index.go`).
- Only a text change bumps a document's revision; state-only changes must not,
  or every sync re-embeds the corpus (`internal/semantic/mirror.go::Index.SyncMirror`).
- Passes start at least a minute apart, wakes included; every sync completion
  wakes the indexer, and each pass rereads every description and comment
  (`internal/semantic/indexer.go::schedulePasses`).
- Skip a document only when the endpoint refuses that input; any other encode
  failure aborts the pass so an outage never stamps the corpus as skipped
  (`internal/semantic/indexer.go::inputRejected`).
- Publish the configured generation before its backfill, even after a model
  change: queries use the configured model, so an older generation's vectors
  must never answer (`internal/semantic/encoder.go::Encoder.owns`).

## Search

- Search is exact with tracked-repository, item-type, and state filters applied
  before ranking, one hit per item; cost grows with eligible chunks
  (`internal/semantic/search.go::Indexer.Search`).
- A filter naming an untracked repository is a validation error, not an empty
  result (`internal/server/semanticapi/handler.go::Handler.repoIDs`).
- Scores only order one response; `ready` is false while results cover a
  partial backfill.
