# Gitea Container Fixture

This opt-in fixture starts a real Gitea instance on loopback, runs an idempotent bootstrap script, and lets the Go e2e test sync seeded data into SQLite.

Run:

```sh
KENN_FORGE_GITEA_CONTAINER_TESTS=1 go test ./internal/server -run TestGiteaContainerSync -shuffle=on
```

The manifest emitted by `bootstrap.sh` includes both `base_url` and `api_url`. The provider test intentionally configures the SDK with `base_url`; `api_url` is only for script/API diagnostics.
