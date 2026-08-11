# ADR 0005: Disable Ephemeral Sync by Default

## Status

Accepted

## Context

`make dev-ephemeral` starts a second server from copied state. Running its normal
provider sync in parallel can spend the maintainer's limited provider quota.
Removing credentials would also disable intentional foreground provider work.

## Decision

Ephemeral development disables sync by default. A developer can opt in with:

```sh
make dev-ephemeral ARGS="-sync"
```

The transient `serve --disable-sync` option selects this policy; it does not alter
TOML. Sync and refresh code receives a gated view of the provider registry. The raw
registry remains available to intentional foreground operations and mutations.
Admission endpoints reject asynchronous work that cannot complete while sync is
disabled, including archive starts, notification read propagation, and deferred
merges.

## Consequences

- Starting the default ephemeral stack does not spend provider quota on sync.
- Cached data and direct provider operations remain available.
- Sync-dependent endpoints can return service unavailable.
- Successful direct provider mutations can leave copied cached state stale until
  sync is enabled.
