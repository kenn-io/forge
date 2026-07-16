# Bound disposable Kata Unix connections

Approved in design discussion on 2026-07-16.

## Goal

Stop server-side Kata reads from accumulating idle Unix-socket connections,
and roll out the existing frontend catch-up batching fix so reconnecting a
browser cannot turn a historical event backlog into a sustained daemon CPU
storm.

## Observed behavior

Two independent behaviors amplify each other after a long-running local
deployment reconnects:

1. An installed frontend predating middleman commit `56e34a95` revalidates the
   current Kata view once per historical event while catching up. Current
   `main` already collapses that catch-up into one view revalidation.
2. Each server-side read constructs and then abandons an HTTP transport. For a
   Unix target, that transport has no idle-connection timeout, so every
   keep-alive connection remains parked after its response body closes.

The task-detail path starts an issue read and a best-effort projects read, so
it can leave two idle connections per refresh. The project-mappings path can
leave one. The health probe has the same behavior for Unix targets, although
its cache bounds the probe rate. TCP targets do not exhibit the unbounded
version of the leak because their disposable default transports expire idle
connections after 90 seconds; HTTP health probes share `http.DefaultTransport`.

## Transport lifecycle

Keep `kataDaemonProxyTarget` as the shared target parser for both the cached
reverse proxy and disposable server-side clients. It must not disable
keep-alives itself: the reverse proxy owns a cached transport and should
continue reusing its connections.

On the disposable side, clone each concrete `*http.Transport` and set
`DisableKeepAlives` on the clone:

- `kataDaemonHTTPClient` applies the disposable policy after resolving either
  its Unix transport or its per-client default TCP transport.
- `probeKataDaemon` applies the policy only when `kataDaemonProbeTarget`
  returns an owned transport. Thus Unix probes close their connection after
  the response, while HTTP and HTTPS probes continue using the shared default
  transport.

A small helper will contain the type assertion and clone so the ownership
boundary is explicit and both disposable call sites use the same policy. No
mutation reaches the transport returned to the cached reverse proxy.

With keep-alives disabled, closing a response body closes its underlying
connection. This also covers the best-effort projects request that may finish
after the task-detail handler returns: the connection closes when that
goroutine closes its own response body, without requiring the handler to join
it.

## Alternatives considered

- **Call `CloseIdleConnections` when the handler returns.** This is
  insufficient. It closes connections that are idle at that moment, but the
  detached projects request may still be in flight and become idle only after
  the call, leaving that connection parked.
- **Cache one client per daemon.** This could make keep-alives safe, but adds
  cache lifecycle and invalidation concerns around target and credential
  changes. Disposable local reads are low-volume enough that a Unix dial per
  request is the simpler ownership model.
- **Disable keep-alives in `kataDaemonProxyTarget`.** Rejected because that
  helper also supplies the cached reverse proxy, where connection reuse is
  intentional.

## Regression coverage

Use a real HTTP server listening on a Unix socket under `t.TempDir()` and track
connections with `http.Server.ConnState`. Tests exercise requests rather than
asserting implementation text:

- A task-detail request performs both upstream reads, including a delayed
  best-effort projects response, and all connections drain to zero after the
  reads finish.
- A project-mappings request drains its upstream connection to zero.
- A Unix health probe drains its upstream connection to zero.
- Existing reverse-proxy tests continue to cover the cached proxy path; its
  transport remains keep-alive capable.

The connection assertions will wait on bounded state transitions rather than
relying on arbitrary sleeps. Tests use only temporary local sockets and
synthetic daemon responses.

## Rollout and verification

Build and install current `main`, which includes both this transport change
and the existing frontend catch-up batching commit. Restart middleman, reopen
the same browser workload, and observe the Kata daemon CPU and both processes'
Unix-socket descriptor counts over repeated refreshes. CPU should settle after
catch-up and descriptor counts should remain bounded.

The two outcomes are checked independently: the current frontend revision
prevents per-event revalidation, while connection counts demonstrate that the
server-side transport lifecycle is fixed. No Kata database, daemon
configuration, or reverse-proxy connection policy changes are required.
