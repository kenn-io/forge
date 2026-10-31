# Local observability stack

A persistent trace, metric, and log store for a kenn-forge development host.
One [`grafana/otel-lgtm`](https://github.com/grafana/docker-otel-lgtm)
container runs the OpenTelemetry Collector, Tempo (traces), Prometheus
(metrics), Loki (logs), and Grafana. Data lives in the `lgtm-data` volume and
is kept for 7 days.

## What reports to it

| Source | Service name | How |
| --- | --- | --- |
| Daemon (`kenn-forge serve`) | `kenn-forge` | OTLP from the `OTEL_*` environment |
| ACP owners and shared agent hosts | `kenn-forge-acp-owner`, `kenn-forge-acp-host` | Inherit the daemon's environment; their start spans join the trace that launched them |
| Browser SPA | `kenn-forge-web` | Posts OTLP JSON to the daemon (`POST /api/v1/telemetry/traces`), which forwards it to the same collector |

The browser never talks to the collector. It exports only while the daemon
exports traces over OTLP/HTTP, so the collector needs no browser-reachable
address and no CORS setup.

## Run it

```bash
docker compose -f tools/observability/compose.yaml up -d   # or: make otel-lgtm
```

Grafana listens on `http://127.0.0.1:3000` and OTLP on `127.0.0.1:4317`
(gRPC) and `127.0.0.1:4318` (HTTP). Every port binds to localhost. Grafana
allows anonymous Admin access, the image's default, so publish it only on a
private network.

## Point the daemon at it

Set these in the daemon's environment and restart it:

```bash
OTEL_TRACES_EXPORTER=otlp
OTEL_METRICS_EXPORTER=otlp
OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318
OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
```

For a systemd user unit, put them in a drop-in such as
`~/.config/systemd/user/kenn-forge.service.d/otel.conf`:

```ini
[Service]
Environment=OTEL_TRACES_EXPORTER=otlp
Environment=OTEL_METRICS_EXPORTER=otlp
Environment=OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318
Environment=OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
```

Then `systemctl --user daemon-reload && systemctl --user restart kenn-forge`.
Do not set `OTEL_SERVICE_NAME` there: the daemon passes its environment to the
agents it launches, and each process names itself.

## Reach Grafana from other machines

Keep the ports on localhost and publish Grafana over Tailscale:

```bash
GRAFANA_ROOT_URL=https://<host>.<tailnet>.ts.net:3443 \
  docker compose -f tools/observability/compose.yaml up -d
tailscale serve --bg --https=3443 http://127.0.0.1:3000
```

`GRAFANA_ROOT_URL` makes Grafana's links and redirects use the published
address. `tailscale serve --https=3443 off` removes the route.

Publish the collector's OTLP/HTTP port the same way so daemons on other
machines can export to it:

```bash
tailscale serve --bg --https=4318 http://127.0.0.1:4318
```

They then set `OTEL_EXPORTER_OTLP_ENDPOINT=https://<host>.<tailnet>.ts.net:4318`.
Tailscale terminates TLS, so no exporter needs certificate settings.

## Find slow work

In Grafana, open Explore, choose Tempo, and search with TraceQL:

```text
{ resource.service.name = "kenn-forge" && duration > 5s }
{ name = "workspace.setup" }
{ span.workspace.id = "<workspace id>" }
```

Spans worth knowing:

- `workspace.setup` and its `workspace.setup.<stage>` children. A
  `workspace.repo_lock` span's `repository lock acquired` event marks the end
  of its wait for the per-repository lock.
- `exec <binary> [git subcommand]` for every subprocess, including the wait
  for subprocess capacity (the `subprocess capacity acquired` event).
- `workspace.acp.reconnect` (its `restore lock acquired` event marks the end
  of the wait for the daemon-wide restore lock), `acp.restore`,
  `acp.owner.replace`, `acp.owner.start`, and `acp.owner.attach`.
- In the owner and host processes: `acp.owner.run`, `acp.agent.start`,
  `acp.host.dial`, `acp.host.start`, `acp.host.run`, and the agent handshake
  steps `acp.agent.initialize`, `acp.agent.session_new`, and
  `acp.agent.session_load`.
- Browser `GeneratedApi.*` spans are the parents of the daemon's HTTP spans
  for the requests they make.

## Agents

[`mcp-grafana`](https://github.com/grafana/mcp-grafana) gives coding agents
read access to the same data. With anonymous access it needs no token. For
Claude Code:

```bash
claude mcp add --scope user grafana -- docker run --rm -i --network host \
  -e GRAFANA_URL=http://127.0.0.1:3000 grafana/mcp-grafana:2.1.1 -t stdio \
  --disable-write --enabled-tools search,datasource,prometheus,loki,tempo,dashboard,navigation
```

## Stop or reset

```bash
docker compose -f tools/observability/compose.yaml down      # keep data
docker compose -f tools/observability/compose.yaml down -v   # delete data
```
