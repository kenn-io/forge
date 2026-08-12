# Config Persistence Invariants

Use this document when adding or changing config fields that kenn-forge saves
back to TOML.

- `configFile` in `internal/config/config.go` is the hand-maintained subset of `Config` that `Save` writes to disk. A `Config` field absent from `configFile` (or from the `Save` initializer) loads from TOML fine but is silently dropped on the next save or restart.
- Every new persisted config field or section must be wired in three places — `Config`, `configFile`, and the `Save` initializer — and covered by a save/load round-trip test with a non-default value (see `TestPullRequestsConfigRoundTrip` in `internal/config/config_test.go`).
- When zero is meaningful, represent the saved value as optional so TOML `omitempty` cannot turn explicit zero into an unset default; the round-trip test must cover zero (`internal/config/config.go::Terminal`).
- Repository preset config stores only named custom definitions; `Global` is a derived UI preset and must never be serialized to TOML (`internal/config/config.go::RepoPreset`).
