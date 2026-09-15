# Activity relay

`kenn-forge-relay` collects signed GitHub webhooks and serves durable refresh
hints to Forge instances over a private network. Build it without a frontend:

```sh
make build-relay
tmp/kenn-forge-relay --config /path/to/relay.toml
```

Linux releases include `forge-relay_VERSION_linux_ARCH.tar.gz` and checksums.

## Configure and expose

```toml
database = "/var/lib/forge-relay/activity.db"
webhook_listen = "127.0.0.1:8081"
feed_listen = "127.0.0.1:8082"
retention = "168h"

[sources.team]
secret_file = "/run/credentials/forge-relay/team"
repository_ids = [12345]
```

Both listeners require loopback addresses. Put a public HTTPS proxy in front
of **only** `POST /webhooks/github/team` on port 8081. Publish port 8082 through
private HTTPS, for example Tailscale Serve, with tailnet policy granting feed
access to authorized developers. Never expose the feed through the public proxy.
The feed has no separate application authentication; the private transport owns
access. `/healthz` on the feed listener checks storage availability.

Keep verification secrets outside config and SQLite, readable only by the
service. Use a dedicated service account, a private state directory, and umask
0077. A source has one secret and an explicit numeric GitHub repository ID
allowlist. Multiple GitHub Apps or repository hooks can use separate sources;
the service holds no GitHub API credentials.

Configure JSON webhooks with TLS verification and the matching source secret.
Subscribe to `pull_request`, `pull_request_review`, `pull_request_review_comment`,
`pull_request_review_thread`, `issues`, `issue_comment`, `check_run`,
`check_suite`, `workflow_run`, `push`, `create`, `delete`, and `repository`.
Unassociated check events produce no hints; do not subscribe to `status` or
`workflow_job`. GitHub does not automatically redeliver failed webhooks, so
normal Forge polling remains the recovery path.

## Data and recovery

SQLite retains repository node IDs, target kinds, item numbers, receipt
times, and sequence/stream identifiers. It does not store payloads, names,
titles, bodies, actors, refs, commit hashes, secrets, or delivery IDs. IDs and
activity timing are still metadata: restrict access and use a short retention
window. Do not enable request-body capture in proxies or monitoring.

Forge checkpoints feed pages together with pending refresh targets before
performing provider reads. Each consumer has its own cursor; duplicate hints
coalesce. Restarting the relay preserves the stream. Replacing its database,
expiring a cursor, or connecting for the first time asks Forge to reconcile its
tracked repositories. There is no need to restore the relay database from a
backup. Changing Forge's relay URL requires a restart and a fresh reconciliation.

Before enabling real hooks, exercise valid and invalid signatures, confirm the
public origin rejects `/activity`, and verify allowed and denied tailnet feed
connections. Do not infer access isolation from configuration alone.

## Integration check

```sh
go test ./internal/server -run TestActivityRelayEndToEnd -shuffle=on
```

This builds and launches the separate binary against temporary SQLite files,
sends signed synthetic webhooks, runs the Forge consumer against a local GitHub
HTTP fixture, and checks Forge's API and update events across a relay restart.
The normal CI test and race lanes include it; `-short` skips the subprocess.
