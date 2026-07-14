# W3C trace propagation and OTel export for workspace tracing

Kata: vjh3 (related to epic qyh6). Approved in design discussion on
2026-07-14.

## Goal

Frontend-initiated distributed traces for middleman: the browser mints
standard W3C trace context, every API request and terminal WebSocket
attach joins that trace on the Go server, and an opt-in OTLP exporter
sends server spans to a local Grafana LGTM container. Workspace-switch
traces are linkable from the existing `workspace-switch:*` User Timing
measures by trace ID.

## Decisions already made

- **Propagation-only frontend.** The frontend mints `traceparent` and
  `baggage` headers but exports no spans and gains no dependencies.
  Browser-side timing stays in the User Timing instrumentation added
  for kata vh78; the trace ID in each measure's `detail` is the join
  key. (Considered and rejected: `@opentelemetry/sdk-trace-web` behind
  a dev flag — heavier, needs collector CORS; a hand-rolled OTLP JSON
  exporter — bespoke code for little gain.)
- **WS attach joins the trace** via `traceparent`/`baggage` query
  parameters, because browsers cannot set headers on `new WebSocket()`.
- **kit bootstraps OTel.** Bump `go.kenn.io/kit` v0.1.7 → v0.9.3 and
  use `telemetry.Init`: env-driven exporter selection (`autoexport`),
  default-off export, `tracecontext + baggage` propagators
  (`autoprop`), returned shutdown hook. No hand-rolled
  `internal/tracing` bootstrap. The bump was trial-built against the
  whole repo: middleman's kit surface (`git/cmd`, `git/env`,
  `git/remote`) is API-compatible; transitive bumps include
  `modernc.org/sqlite` 1.52 → 1.53, which the full Go suite must cover.
- **Grafana all-in-one as the local sink**: `grafana/otel-lgtm`
  (OTLP on 4318, UI on 3000) via a make target.

## Frontend

New module `frontend/src/lib/instrumentation/traceContext.ts`:

- Mints IDs with `crypto.getRandomValues` (16-byte trace, 8-byte span);
  formats `traceparent: 00-<traceId>-<spanId>-01` and a percent-encoded
  `baggage` string.
- Two granularities:
  - **Interaction trace**: `beginWorkspaceSwitch` opens one (baggage:
    `interaction=workspace-switch`, `workspace.id`, `host.key`). Every
    API request while it is live is a child: same trace ID, fresh span
    ID per request. The trace ID is added to every `workspace-switch:*`
    measure detail.
  - **Generic**: outside a live interaction, each API request mints its
    own single-request trace.
- Propagation is always on. Headers cost ~100 bytes on loopback and the
  server no-ops when export is disabled, so there is no frontend flag.

Wiring:

- One middleware on the shared openapi-fetch client attaches both
  headers to every `/api/v1` request.
- Terminal panes append the same values as query parameters on the WS
  URL (`workspace-runtime.ts` path builders or `buildWsUrl`).
- `workspaceSwitchTiming.ts` owns the interaction lifecycle: begin,
  supersede, cancel, and the 30-second recording window already bound
  the interaction; the trace context follows the same lifecycle.

## Go server

- Serve startup calls `kit/telemetry.Init` (shutdown wired alongside
  the profiler's). Export stays off unless `OTEL_TRACES_EXPORTER=otlp`
  (+ `OTEL_EXPORTER_OTLP_ENDPOINT`) is set.
- `otelhttp.NewHandler` wraps the mux root for context extraction and
  HTTP semconv.
- A small Huma middleware registered on each API instance (`/health`,
  `/api/v1`, `/ws/v1`) renames the active span to the matched route
  pattern (otelhttp cannot see `r.Pattern` at span start) and copies
  allow-listed baggage keys (`interaction`, `workspace.id`,
  `host.key`) onto span attributes so Tempo can search by them.
- The WS attach handler extracts query-param trace context and records
  a `terminal.attach` span covering setup up to the streaming loop —
  never the connection lifetime. Auto-instrumentation alone would
  produce hours-long spans for hijacked connections, so `/ws/v1` spans
  end at hijack.
- The e2e server inherits everything through the shared server wiring,
  so the profiling harness composes with tracing for free.

## Collector and docs

- Make target `otel-lgtm` runs the `grafana/otel-lgtm` container.
- `frontend/tests/profiling/README.md` gains a "Live tracing" section:
  start the container, run
  `OTEL_TRACES_EXPORTER=otlp OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318 middleman serve`,
  find a switch's trace by the trace ID in its User Timing detail or by
  the `workspace.id` attribute in Tempo.

## Non-goals

- Browser-side span export.
- Manual spans inside tmux/workspace-list code paths (belongs to kata
  wjtq/acjr, which get the skeleton to hang spans on).
- Metrics or logs pipelines.
- Any default-on telemetry; export requires explicit env opt-in.

## Testing

- **Go** (in-memory span recorder via `sdk/trace/tracetest`): a request
  with `traceparent` produces a server span with that parent; baggage
  keys land as span attributes; span names are route patterns; WS
  query-param context is extracted into the `terminal.attach` span; the
  attach span ends before streaming; with export disabled the request
  path records no spans (no-op provider).
- **Vitest**: traceparent format validity; fresh span ID per request
  within one interaction trace; generic requests get distinct trace
  IDs; baggage percent-encoding; `workspace-switch:*` details carry the
  trace ID; interaction supersede/cancel drops the old trace context.
- **Verification**: run the LGTM container locally, perform a workspace
  switch, and confirm the trace (API spans + terminal.attach) appears
  in Tempo under the trace ID reported in the browser measures. The
  full Go and frontend suites cover the kit and transitive sqlite
  bumps.
