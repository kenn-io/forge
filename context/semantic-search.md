# Semantic Search

Use this document when changing the embedding index, its configuration, or
semantic ranking of the pull request and issue lists.

## Ownership

- `[embeddings]` is kit's `embedconfig.Embedder`; the zero value disables
  semantic search and is omitted on save. Put Forge-specific settings beside
  it, never inside it (`internal/config/config.go::Config`).
- `[embeddings]` is startup-bound: the encoder and its API key are resolved once
  (`internal/server/configreload/config_reload.go::StartupConfigSnapshot`).
- Only the provider owner builds an index; spokes forward list requests with
  `semantic` to the hub like any other list request (`cmd/kenn-forge/main.go`).
- Semantic search is a ranking mode of the existing list routes and
  `kenn_forge_search_items`, not its own API: every list filter, repository
  route resolution, and hub forwarding come from the list path. Add new
  filters there, not to the index (`internal/server/pullapi/services.go::Handler.listPullsByMeaning`).

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

- The index is a rebuildable sidecar, `semantic.db` under `data_dir`, holding
  one generation for the configured model and recipe. A schema, model, or
  recipe change deletes it rather than migrating, so another model's vectors
  can never answer a query. Keep its tables out of the Forge database
  (`internal/semantic/index.go::Open`).
- Only a text change bumps a document's revision, or every sync re-embeds the
  corpus (`internal/semantic/mirror.go::Indexer.syncMirror`).
- Passes start at least a minute apart, wakes included; every sync completion
  wakes the indexer, and each pass rereads every description and comment
  (`internal/semantic/indexer.go::schedulePasses`).
- Skip a document only when the endpoint refuses that input; any other encode
  failure aborts the pass so an outage never stamps the corpus as skipped
  (`internal/semantic/indexer.go::inputRejected`).

## Ranking

- The list applies its filters without `q` or paging, then `semantic.Rank`
  scores exactly those rows and pages the ranking; cost grows with the
  filtered rows' chunks (`internal/semantic/rank.go::Rank`).
- Items with nothing embedded yet are left out, so results are partial during
  a backfill. `search_score` only orders one response.
- The command palette's Meaning mode ranks through the same list routes; it
  keeps commands and docs local and shows the server's problem detail when
  semantic search is unavailable
  (`frontend/src/lib/stores/keyboard/palette-semantic.ts::rankByMeaning`).
