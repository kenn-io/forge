# ADR 0005: Disable Ephemeral Sync by Default

## Status

Accepted

## Context

`make dev-ephemeral` starts a second kenn-forge server from a copied database and
generated config. That server currently starts the same provider sync work as the
normal server, so parallel development can spend the maintainer's limited provider
API quota without an explicit decision to do so.

A very long sync interval would still permit manual and indirect refreshes. Removing
provider credentials would prevent sync but also break unrelated provider-backed
development workflows. Persisting the policy in TOML would unnecessarily expand a
development safety default into the normal configuration contract.

## Decision

Ephemeral development will disable all sync activity by default. A developer can
restore normal behavior explicitly with:

```sh
make dev-ephemeral ARGS="-sync"
```

The `serve` command will expose a transient `--disable-sync` option. The ephemeral
launcher will pass it to the backend unless `-sync` is present. Normal `serve`
launches, the source config, and the generated config schema will not change.

The server will retain its provider registry and copied database so cached data and
non-sync development workflows remain available. The central sync policy will block:

- periodic repository, watched-item, archive, and notification work;
- queued notification propagation;
- explicit repository, pull-request, issue, CI, and notification refreshes;
- resolve-on-miss and post-mutation provider refreshes.

Sync-triggering HTTP calls will return a service-unavailable problem instead of
accepting work that cannot run. The status JSON remains process-discovery data and
will not gain a sync-policy field. Changing `-sync` for a live work directory
requires stopping and restarting its stack.

## Consequences

### Positive

- Starting the default ephemeral stack does not spend provider quota on sync.
- Opting into production-like sync behavior remains a one-flag choice.
- Cached provider data remains available for UI and local workflow development.
- One server-side policy covers background, manual, and indirect sync entry points.

### Negative

- Sync controls can surface a service-unavailable response in the default stack.
- Notification acknowledgements and deferred merges return service unavailable
  because both require background provider refresh work to complete reliably.
- Successful direct provider mutations can leave copied cached state stale until
  the ephemeral stack is restarted from newly copied data or sync is enabled.
- The central policy must guard new sync entry points as they are introduced.
- Provider initialization and intentional non-sync operations can still use
  credentials; this is not a general offline mode.

## Verification

Tests will cover launcher default and opt-in command construction, serve flag wiring,
the absence of background sync loops, and representative manual and indirect sync
rejection before provider access. The existing ephemeral workflow, testify-helper,
and Make help checks remain required.
