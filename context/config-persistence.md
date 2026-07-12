# Config Persistence Invariants

- `configFile` in `internal/config/config.go` is the hand-maintained subset of `Config` that `Save` writes to disk. A `Config` field absent from `configFile` (or from the `Save` initializer) loads from TOML fine but is silently dropped on the next save or restart.
- Every new persisted config field or section must be wired in three places — `Config`, `configFile`, and the `Save` initializer — and covered by a save/load round-trip test with a non-default value (see `TestPullRequestsConfigRoundTrip` in `internal/config/config_test.go`).
