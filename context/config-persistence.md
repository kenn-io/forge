# Config Persistence Invariants

Use this document when adding or changing config fields that kenn-forge saves
back to TOML.

- `configFile` in `internal/config/config.go` is the hand-maintained subset of `Config` that `Save` writes to disk. A `Config` field absent from `configFile` (or from the `Save` initializer) loads from TOML fine but is silently dropped on the next save or restart.
- Every new persisted config field or section must be wired in three places — `Config`, `configFile`, and the `Save` initializer — and covered by a save/load round-trip test with a non-default value (see `TestPullRequestsConfigRoundTrip` in `internal/config/config_test.go`).
- `activity.use_workspace_activity_for_recency` is one global PR, Issue, and Activity opt-in. Its zero value is intentionally false; successful settings writes and file reloads publish the committed value to handler snapshots (`internal/server/server.go::pullConfigSnapshot`).
- `detail.initial_timeline_entry_limit` is a global PR/issue presentation preference.
  Omitted or zero values default to 50; explicit values must remain within 10-250
  in both config loading and settings writes (`internal/config/config.go::Detail`).
- When zero is meaningful, represent the saved value as optional so TOML `omitempty` cannot turn explicit zero into an unset default; the round-trip test must cover zero (`internal/config/config.go::Terminal`).
- Whole-file settings mutations must hold `configReloadMu` before `cfgMu` while applying and saving changes, or the watcher can restore a stale snapshot between writes (`internal/server/settings_handlers.go::updateSettings`).
- Partial settings request objects must use pointer fields and merge only fields that were present; reusing persisted value structs collapses omission into zero values (`internal/server/settings_handlers.go::mcpSettingsUpdate`).
- `roborev.init_managed_clones` is a hot-reloaded, false-by-default setup policy. It persists through the partial `roborev` settings object and the committed workspace API snapshot; only the effective Roborev endpoint remains in the startup-bound restart snapshot (`internal/config/config.go::Roborev`, `internal/server/config_reload.go::startupConfigSnapshot`, `internal/server/workspaceapi/config.go::ConfigSnapshot`).
- Repository preset config stores only named custom definitions; `Global` is a derived UI preset and must never be serialized to TOML. Each member persists provider, provider host, provider-verified repository ID, and a last-known display route; preset create/update/delete use dedicated atomic settings endpoints instead of replacing the collection through generic settings (`internal/config/config.go::RepoPreset`, `internal/server/settings_handlers.go::mutateRepoPresets`).
