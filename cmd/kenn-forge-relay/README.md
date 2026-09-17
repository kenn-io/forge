# Activity relay

`kenn-forge-relay` receives GitHub webhooks and passes each change to the
connected Forges so they can refresh changed items between normal syncs.
Delivery is best effort; a Forge that misses a change catches up through
normal syncing. The relay runs as a separate stateless service without a
frontend or database.

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

This builds and launches the separate binary, sends signed synthetic webhooks,
runs the Forge subscriber against a local GitHub HTTP fixture, and checks
Forge's API and update events across a relay restart. The normal CI test and
race lanes include it; `-short` skips the subprocess.
