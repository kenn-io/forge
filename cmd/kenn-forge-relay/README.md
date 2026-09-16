# Activity relay

`kenn-forge-relay` receives GitHub webhooks so Forge can refresh changed items
between normal syncs. It runs as a separate service without a frontend.

For service setup, GitHub webhooks, private feed access, and a real update
walkthrough, see [Faster GitHub updates](../../docs/activity-relay.md).

## Build from source

```sh
make build-relay
tmp/kenn-forge-relay --config /path/to/relay.toml
```

## Integration check

```sh
go test ./internal/server -run TestActivityRelayEndToEnd -shuffle=on
```

This builds and launches the separate binary against temporary SQLite files,
sends signed synthetic webhooks, runs the Forge consumer against a local GitHub
HTTP fixture, and checks Forge's API and update events across a relay restart.
The normal CI test and race lanes include it; `-short` skips the subprocess.
