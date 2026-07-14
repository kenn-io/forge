# W3C Trace Propagation and OTel Export Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Frontend-minted W3C trace context on every API request and terminal WS attach, exported from the Go server via env-gated OTLP, joinable to the `workspace-switch:*` User Timing measures by trace ID.

**Architecture:** Propagation-only frontend (no browser span export): a small `traceContext.ts` module mints traceparent/baggage, an openapi-fetch middleware attaches them, and the workspace-switch interaction owns a trace whose ID lands in measure details. The Go server bootstraps OTel via `kit/telemetry.Init` (default-off export), wraps the mux in `otelhttp` (filtered to exclude WS upgrades and SSE), renames spans to route patterns via Huma middleware, and records bounded `terminal.attach` spans from query-param trace context.

**Tech Stack:** `go.kenn.io/kit v0.9.3` (`telemetry.Init`), `go.opentelemetry.io/contrib/.../otelhttp`, `otel/trace|baggage|attribute|propagation`, `sdk/trace/tracetest` (tests), openapi-fetch middleware, Web Crypto.

Spec: `docs/superpowers/specs/2026-07-14-otel-trace-propagation-design.md` (kata vjh3).

## Global Constraints

- Export must default OFF; enabling requires `OTEL_TRACES_EXPORTER=otlp` (+ `OTEL_EXPORTER_OTLP_ENDPOINT`). No default-on telemetry.
- No new frontend dependencies; no browser span export.
- Baggage keys carried to span attributes: exactly `interaction`, `workspace.id`, `host.key`.
- No hours-long spans: WS upgrades and `/events` SSE are excluded from otelhttp; `terminal.attach` spans end before streaming loops.
- Go tests: testify, `-shuffle=on`, no `t.Fatal`/`t.Error`; wire-level via `srv.ServeHTTP` per `context/testing.md`.
- Frontend: never npm; `vp` tooling; fmt only named files.
- Commit after each task (kenn:commit conventions).

---

### Task 1: kit bump + telemetry.Init in both server binaries

**Files:**
- Modify: `go.mod` / `go.sum` (kit v0.1.7 → v0.9.3; promotes otel deps)
- Modify: `cmd/middleman/main.go` (~line 663, beside `profiler.Start`)
- Modify: `cmd/e2e-server/main.go` (in `run()`, beside the pprof block ~line 1990)

**Interfaces:**
- Produces: global OTel tracer provider + `tracecontext,baggage` propagators registered at startup in both binaries; `telemetry.Init(ctx) (shutdown func(context.Context) error, err)`.

- [ ] **Step 1:** `go get go.kenn.io/kit@v0.9.3 && go mod tidy`, then `go build ./...` (expected: clean; transitive `modernc.org/sqlite` 1.52→1.53 rides along).
- [ ] **Step 2:** In `cmd/middleman/main.go`, immediately before the profiler block, mirror the profiler's error style:

```go
otelShutdown, err := telemetry.Init(ctx)
if err != nil {
    return fmt.Errorf("initialize telemetry: %w", err)
}
defer func() {
    shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    if err := otelShutdown(shutdownCtx); err != nil {
        slog.Warn("telemetry shutdown failed", "err", err)
    }
}()
```

(import `go.kenn.io/kit/telemetry`; check the profiler's actual error handling at main.go:664 and match its return-vs-warn choice.)
- [ ] **Step 3:** Same pattern in `cmd/e2e-server/main.go` `run()` before the pprof block, but warn-and-continue on Init error (the e2e suite must not die to a bad OTEL env var).
- [ ] **Step 4:** Run `make test` (full suite — covers the sqlite driver bump). Expected: pass (modulo documented local PTY baseline flakes; A/B if anything new fails).
- [ ] **Step 5:** Commit: `feat: bootstrap OpenTelemetry via kit telemetry.Init`.

### Task 2: query-param trace carrier + attach-span helper

**Files:**
- Create: `internal/tracing/tracing.go`
- Test: `internal/tracing/tracing_test.go`

**Interfaces:**
- Produces:
  - `func QueryCarrier(values url.Values) propagation.TextMapCarrier` — reads `traceparent`/`baggage` from query params (case-insensitive keys not required; exact lowercase).
  - `func StartAttachSpan(r *http.Request, name string) (context.Context, trace.Span)` — extracts trace context+baggage from `r.URL.Query()` via the global propagator, starts a span with tracer `"go.kenn.io/middleman/internal/tracing"`, copies the allow-listed baggage keys (`interaction`, `workspace.id`, `host.key`) to same-named string attributes.

- [ ] **Step 1:** Write failing tests (`internal/tracing/tracing_test.go`):

```go
package tracing

import (
    "net/http/httptest"
    "testing"

    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
    "go.opentelemetry.io/otel"
    "go.opentelemetry.io/otel/propagation"
    sdktrace "go.opentelemetry.io/otel/sdk/trace"
    "go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestStartAttachSpanExtractsQueryParamContext(t *testing.T) {
    require := require.New(t)
    assert := assert.New(t)

    recorder := tracetest.NewSpanRecorder()
    prev := otel.GetTracerProvider()
    prevProp := otel.GetTextMapPropagator()
    otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))
    otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
        propagation.TraceContext{}, propagation.Baggage{},
    ))
    t.Cleanup(func() {
        otel.SetTracerProvider(prev)
        otel.SetTextMapPropagator(prevProp)
    })

    r := httptest.NewRequest("GET",
        "/ws/v1/workspaces/abc/terminal"+
            "?traceparent=00-11111111111111111111111111111111-2222222222222222-01"+
            "&baggage=interaction%3Dworkspace-switch%2Cworkspace.id%3Dabc",
        nil)
    _, span := StartAttachSpan(r, "terminal.attach")
    span.End()

    spans := recorder.Ended()
    require.Len(spans, 1)
    assert.Equal("terminal.attach", spans[0].Name())
    assert.Equal("11111111111111111111111111111111", spans[0].SpanContext().TraceID().String())
    assert.Equal("2222222222222222", spans[0].Parent().SpanID().String())
    attrs := map[string]string{}
    for _, kv := range spans[0].Attributes() {
        attrs[string(kv.Key)] = kv.Value.AsString()
    }
    assert.Equal("workspace-switch", attrs["interaction"])
    assert.Equal("abc", attrs["workspace.id"])
}

func TestStartAttachSpanWithoutParamsStartsRootSpan(t *testing.T) {
    // same provider/propagator setup as above
    // request with no query params -> span still returned, no parent,
    // no baggage attributes; assert spans[0].Parent().IsValid() == false
}
```

(Write the second test out fully with the same setup; assert `assert.False(spans[0].Parent().IsValid())` and `assert.Empty(attrs["interaction"])`.)
- [ ] **Step 2:** `go test ./internal/tracing -shuffle=on` → FAIL (package missing).
- [ ] **Step 3:** Implement `internal/tracing/tracing.go`:

```go
// Package tracing holds small OpenTelemetry helpers shared by the
// HTTP server and terminal attach handlers. Provider/exporter
// bootstrap lives in kit's telemetry.Init, not here.
package tracing

import (
    "context"
    "net/http"
    "net/url"

    "go.opentelemetry.io/otel"
    "go.opentelemetry.io/otel/attribute"
    "go.opentelemetry.io/otel/baggage"
    "go.opentelemetry.io/otel/propagation"
    "go.opentelemetry.io/otel/trace"
)

// BaggageAttributeKeys are the baggage entries copied onto server
// spans so traces are searchable by them.
var BaggageAttributeKeys = []string{"interaction", "workspace.id", "host.key"}

type queryCarrier struct{ values url.Values }

func (c queryCarrier) Get(key string) string { return c.values.Get(key) }
func (c queryCarrier) Set(string, string)    {}
func (c queryCarrier) Keys() []string {
    keys := make([]string, 0, len(c.values))
    for k := range c.values {
        keys = append(keys, k)
    }
    return keys
}

// QueryCarrier adapts URL query parameters as a propagation carrier.
// Browsers cannot set headers on WebSocket handshakes, so terminal
// attach URLs carry traceparent/baggage as query parameters instead.
func QueryCarrier(values url.Values) propagation.TextMapCarrier {
    return queryCarrier{values: values}
}

// SetBaggageAttributes copies the allow-listed baggage entries from
// ctx onto span as string attributes.
func SetBaggageAttributes(ctx context.Context, span trace.Span) {
    bag := baggage.FromContext(ctx)
    for _, key := range BaggageAttributeKeys {
        if value := bag.Member(key).Value(); value != "" {
            span.SetAttributes(attribute.String(key, value))
        }
    }
}

// StartAttachSpan extracts trace context and baggage from r's query
// parameters and starts a span. Callers must End the span before
// entering any long-lived streaming loop so attach spans stay bounded.
func StartAttachSpan(r *http.Request, name string) (context.Context, trace.Span) {
    ctx := otel.GetTextMapPropagator().Extract(r.Context(), QueryCarrier(r.URL.Query()))
    ctx, span := otel.Tracer("go.kenn.io/middleman/internal/tracing").Start(ctx, name)
    SetBaggageAttributes(ctx, span)
    return ctx, span
}
```
- [ ] **Step 4:** `go test ./internal/tracing -shuffle=on` → PASS.
- [ ] **Step 5:** Commit: `feat: add query-param trace carrier for terminal attach spans`.

### Task 3: otelhttp wrap + Huma span-naming middleware

**Files:**
- Modify: `internal/server/server.go` (handler assembly ~lines 854-888)
- Create: `internal/server/otel_middleware.go`
- Test: `internal/server/otel_middleware_test.go` (wire-level)

**Interfaces:**
- Consumes: `tracing.SetBaggageAttributes` (Task 2).
- Produces: every non-WS, non-SSE HTTP request gets a server span named `<METHOD> <route pattern>` parented on the incoming `traceparent`, with baggage attributes.

- [ ] **Step 1:** Write the failing wire test. Use the package's existing server-construction test helpers (see how `compression_test.go` and `internal/server/apitest` build a server; route through `ServeHTTP`):

```go
func TestHTTPSpansParentedOnTraceparent(t *testing.T) {
    require := require.New(t)
    assert := assert.New(t)

    recorder := tracetest.NewSpanRecorder()
    prev := otel.GetTracerProvider()
    prevProp := otel.GetTextMapPropagator()
    otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))
    otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
        propagation.TraceContext{}, propagation.Baggage{},
    ))
    t.Cleanup(func() {
        otel.SetTracerProvider(prev)
        otel.SetTextMapPropagator(prevProp)
    })

    srv := newTestServer(t) // reuse the package's existing helper for a minimal server

    req := httptest.NewRequest("GET", "/api/v1/health", nil) // pick a real registered route
    req.Header.Set("traceparent", "00-33333333333333333333333333333333-4444444444444444-01")
    req.Header.Set("baggage", "interaction=workspace-switch,workspace.id=ws-9")
    rec := httptest.NewRecorder()
    srv.ServeHTTP(rec, req)

    var got sdktrace.ReadOnlySpan
    for _, s := range recorder.Ended() {
        if s.SpanContext().TraceID().String() == "33333333333333333333333333333333" {
            got = s
        }
    }
    require.NotNil(got, "no span recorded for the injected trace id")
    assert.Equal("4444444444444444", got.Parent().SpanID().String())
    assert.Contains(got.Name(), "GET ")
    attrs := map[string]string{}
    for _, kv := range got.Attributes() {
        if kv.Value.Type() == attribute.STRING {
            attrs[string(kv.Key)] = kv.Value.AsString()
        }
    }
    assert.Equal("ws-9", attrs["workspace.id"])
}
```

Adjust the helper name and route to what the package actually provides (find one while writing; `internal/server` has many `newTestServer`-style constructors). Filter assertions by injected trace ID so concurrent tests can't pollute.
- [ ] **Step 2:** Run: `go test ./internal/server -run TestHTTPSpansParentedOnTraceparent -shuffle=on` → FAIL (no span recorded).
- [ ] **Step 3:** Implement `internal/server/otel_middleware.go`:

```go
package server

import (
    "net/http"
    "strings"

    "github.com/danielgtaylor/huma/v2"
    "go.opentelemetry.io/otel/trace"

    "go.kenn.io/middleman/internal/tracing"
)

// otelTraceable reports whether a request should get an otelhttp
// server span. WebSocket upgrades and the SSE events stream live for
// the connection lifetime and would produce hours-long spans;
// terminal attach gets its own bounded span instead.
func otelTraceable(r *http.Request) bool {
    if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
        return false
    }
    return !strings.HasSuffix(r.URL.Path, "/events")
}

// otelSpanMiddleware renames the otelhttp-created span to the matched
// Huma route pattern (otelhttp cannot see it at span start) and copies
// allow-listed baggage onto it.
func otelSpanMiddleware(ctx huma.Context, next func(huma.Context)) {
    span := trace.SpanFromContext(ctx.Context())
    if span.IsRecording() {
        if op := ctx.Operation(); op != nil {
            span.SetName(ctx.Method() + " " + op.Path)
        }
        tracing.SetBaggageAttributes(ctx.Context(), span)
    }
    next(ctx)
}
```
- [ ] **Step 4:** Wire it in `internal/server/server.go`:
  - After `api.UseMiddleware(newResponseCompressionMiddleware(...))` (line 855): `api.UseMiddleware(otelSpanMiddleware)`; also `healthAPI.UseMiddleware(otelSpanMiddleware)` and, in the workspaces block, `wsAPI.UseMiddleware(otelSpanMiddleware)`.
  - At final assembly (lines 879-887), wrap once before assignment:

```go
handler := otelhttp.NewHandler(assembled, "middleman.http",
    otelhttp.WithFilter(otelTraceable))
s.handler = handler
```

where `assembled` is the previous `outer`/`mux` value in each branch (wrap in both branches, or restructure to wrap once after the if/else).
- [ ] **Step 5:** `go test ./internal/server -run TestHTTPSpansParentedOnTraceparent -shuffle=on` → PASS; then full `go test ./internal/server -shuffle=on` → PASS.
- [ ] **Step 6:** Commit: `feat: trace API requests with route-named OTel server spans`.

### Task 4: terminal.attach spans in WS handlers

**Files:**
- Modify: `internal/terminal/handler.go` (`ServeHTTP`, span from start of setup, End before bridge loops; `websocket.Accept` at line 100)
- Modify: `internal/server/workspace_runtime_terminal.go` (`handleWorkspaceRuntimeSessionTerminal` line 25 / `serveRuntimeTerminal` line 75, End before the bridge)

**Interfaces:**
- Consumes: `tracing.StartAttachSpan(r, "terminal.attach")` (Task 2).
- Produces: bounded `terminal.attach` spans parented on the frontend's WS query-param trace context, with `workspace.id` attribute.

- [ ] **Step 1:** In `internal/terminal/handler.go` `ServeHTTP`, at the top:

```go
ctx, attachSpan := tracing.StartAttachSpan(r, "terminal.attach")
r = r.WithContext(ctx)
attachDone := func() { attachSpan.End() }
```

Call `attachDone()` (guarded by a `sync.Once` or a simple bool if single-threaded at that point) right before entering the streaming/bridge phase in both the pty-owner and tmux paths, and on every early-return error path (simplest: `defer attachSpan.End()` won't work — it would span the whole connection; instead End explicitly after `EnsureTmux`/`AttachPtyOwnerTerminal` succeed and before the bridge loops, plus `attachSpan.End()` before each early `return`). Record errors with `attachSpan.SetAttributes(attribute.Bool("error", true))` on failure paths where a response error is written.
- [ ] **Step 2:** Same shape in `handleWorkspaceRuntimeSessionTerminal`: start span at function top, End after `AttachSessionWithOptions` succeeds and `serveRuntimeTerminal` has accepted the socket and completed initial resize (pass the span or End before calling the bridge section — pick the seam where setup ends at `workspace_runtime_terminal.go:95-124`).
- [ ] **Step 3:** `go build ./... && go test ./internal/terminal ./internal/server -shuffle=on` → PASS (existing handler tests confirm no behavior change; span assertions are covered by Task 2's helper tests — the wiring here is mechanical).
- [ ] **Step 4:** Commit: `feat: record bounded terminal.attach spans from WS trace context`.

### Task 5: frontend traceContext module

**Files:**
- Create: `frontend/src/lib/instrumentation/traceContext.ts`
- Test: `frontend/src/lib/instrumentation/traceContext.test.ts`

**Interfaces:**
- Produces:
  - `beginInteractionTrace(name: string, attrs: Record<string, string>): string` — returns the trace ID; supersedes any live interaction.
  - `endInteractionTrace(traceId?: string): void` — ends the live interaction (token-guarded like cancelWorkspaceSwitch).
  - `currentInteractionTraceId(): string | null`
  - `traceHeadersForRequest(): { traceparent: string; baggage: string | null }` — interaction-parented when one is live (same trace ID, fresh span ID, interaction baggage), otherwise a fresh single-request trace with `baggage: null`.

- [ ] **Step 1:** Write failing tests:

```ts
import { beforeEach, describe, expect, test } from "vite-plus/test";

import {
  beginInteractionTrace,
  currentInteractionTraceId,
  endInteractionTrace,
  traceHeadersForRequest,
} from "./traceContext.js";

const TRACEPARENT = /^00-([0-9a-f]{32})-([0-9a-f]{16})-01$/;

describe("trace context", () => {
  beforeEach(() => {
    endInteractionTrace();
  });

  test("generic requests mint distinct valid traceparents", () => {
    const a = traceHeadersForRequest();
    const b = traceHeadersForRequest();
    expect(a.traceparent).toMatch(TRACEPARENT);
    expect(b.traceparent).toMatch(TRACEPARENT);
    expect(a.traceparent.slice(3, 35)).not.toBe(b.traceparent.slice(3, 35));
    expect(a.baggage).toBeNull();
  });

  test("requests during an interaction share its trace id with fresh span ids", () => {
    const traceId = beginInteractionTrace("workspace-switch", {
      "workspace.id": "ws 1",
      "host.key": "fleet-a",
    });
    const a = traceHeadersForRequest();
    const b = traceHeadersForRequest();
    expect(a.traceparent.slice(3, 35)).toBe(traceId);
    expect(b.traceparent.slice(3, 35)).toBe(traceId);
    expect(a.traceparent.slice(36, 52)).not.toBe(b.traceparent.slice(36, 52));
    expect(a.baggage).toContain("interaction=workspace-switch");
    expect(a.baggage).toContain("workspace.id=ws%201");
    expect(currentInteractionTraceId()).toBe(traceId);
  });

  test("a new interaction supersedes the previous one", () => {
    const first = beginInteractionTrace("workspace-switch", {});
    const second = beginInteractionTrace("workspace-switch", {});
    expect(first).not.toBe(second);
    expect(currentInteractionTraceId()).toBe(second);
  });

  test("ending with a stale trace id keeps the live interaction", () => {
    const first = beginInteractionTrace("workspace-switch", {});
    const second = beginInteractionTrace("workspace-switch", {});
    endInteractionTrace(first);
    expect(currentInteractionTraceId()).toBe(second);
    endInteractionTrace(second);
    expect(currentInteractionTraceId()).toBeNull();
    expect(traceHeadersForRequest().baggage).toBeNull();
  });
});
```
- [ ] **Step 2:** Run `node ../node_modules/vite-plus/bin/vp test run --project unit src/lib/instrumentation/traceContext.test.ts` (from `frontend/`) → FAIL (module missing).
- [ ] **Step 3:** Implement:

```ts
// W3C trace context minted in the frontend and propagated to the Go
// server (headers on API requests, query params on terminal WS URLs).
// Propagation-only: the browser exports no spans; server spans join
// the IDs minted here, and workspace-switch User Timing details carry
// the trace id as the join key.

interface InteractionTrace {
  traceId: string;
  baggage: string;
}

let currentInteraction: InteractionTrace | null = null;

function randomHex(byteLength: number): string {
  const bytes = new Uint8Array(byteLength);
  crypto.getRandomValues(bytes);
  // An all-zero trace/span id is invalid per W3C trace context.
  if (bytes.every((value) => value === 0)) bytes[0] = 1;
  return Array.from(bytes, (value) => value.toString(16).padStart(2, "0")).join("");
}

function encodeBaggage(entries: Record<string, string>): string {
  return Object.entries(entries)
    .filter(([, value]) => value !== "")
    .map(([key, value]) => `${key}=${encodeURIComponent(value)}`)
    .join(",");
}

export function beginInteractionTrace(name: string, attrs: Record<string, string>): string {
  const traceId = randomHex(16);
  currentInteraction = {
    traceId,
    baggage: encodeBaggage({ interaction: name, ...attrs }),
  };
  return traceId;
}

export function endInteractionTrace(traceId?: string): void {
  if (traceId !== undefined && currentInteraction?.traceId !== traceId) return;
  currentInteraction = null;
}

export function currentInteractionTraceId(): string | null {
  return currentInteraction?.traceId ?? null;
}

export function traceHeadersForRequest(): { traceparent: string; baggage: string | null } {
  const traceId = currentInteraction?.traceId ?? randomHex(16);
  return {
    traceparent: `00-${traceId}-${randomHex(8)}-01`,
    baggage: currentInteraction?.baggage ?? null,
  };
}
```
- [ ] **Step 4:** Run the test file → PASS.
- [ ] **Step 5:** Commit: `feat: mint W3C trace context in the frontend`.

### Task 6: wire propagation into API client, workspace switches, and WS URLs

**Files:**
- Modify: `frontend/src/lib/api/runtime.ts` (client middleware, inside `createRuntimeClient`)
- Modify: `frontend/src/lib/instrumentation/workspaceSwitchTiming.ts` (interaction lifecycle + traceId in details)
- Modify: `frontend/src/lib/instrumentation/workspaceSwitchTiming.test.ts` (traceId assertions)
- Modify: `frontend/src/lib/components/terminal/XtermTerminalPane.svelte` (`appendSizeParams` lines ~117-125)
- Modify: `frontend/src/lib/components/terminal/GhosttyTerminalPane.svelte` (`appendSizeParams` lines ~107-115)

**Interfaces:**
- Consumes: Task 5's `traceHeadersForRequest`, `beginInteractionTrace`, `endInteractionTrace`.
- Produces: `traceparent`/`baggage` on every `/api/v1` request and terminal WS URL; `workspace-switch:*` measure `detail.traceId`.

- [ ] **Step 1:** Extend `workspaceSwitchTiming.test.ts` (failing first):

```ts
test("measure details carry the interaction trace id", () => {
  beginWorkspaceSwitch("ws-1", undefined);
  recordWorkspaceSwitchPhase("workspace-request-start", "ws-1", undefined);
  const detail = (measures("workspace-request-start")[0] as PerformanceMeasure).detail as {
    traceId?: unknown;
  };
  expect(detail.traceId).toBe(currentInteractionTraceId());
  expect(typeof detail.traceId).toBe("string");
});

test("cancelling the switch ends its interaction trace", () => {
  const token = beginWorkspaceSwitch("ws-1", undefined);
  cancelWorkspaceSwitch(token);
  expect(currentInteractionTraceId()).toBeNull();
});
```
- [ ] **Step 2:** Run → FAIL. Implement in `workspaceSwitchTiming.ts`:
  - `WorkspaceSwitch` gains `traceId: string`.
  - `beginWorkspaceSwitch`: `const traceId = beginInteractionTrace("workspace-switch", { "workspace.id": workspaceId, ...(hostKey !== undefined ? { "host.key": hostKey } : {}) });` store it.
  - `cancelWorkspaceSwitch` (both the token path and untargeted path) and the supersede path in `beginWorkspaceSwitch`: call `endInteractionTrace(previous.traceId)`.
  - `recordPhase` detail: add `traceId: sw.traceId`.
- [ ] **Step 3:** Run module tests → PASS.
- [ ] **Step 4:** In `frontend/src/lib/api/runtime.ts`, register middleware inside `createRuntimeClient` before returning:

```ts
import type { Middleware } from "openapi-fetch";
import { traceHeadersForRequest } from "../instrumentation/traceContext.js";

const traceMiddleware: Middleware = {
  onRequest({ request }) {
    const { traceparent, baggage } = traceHeadersForRequest();
    request.headers.set("traceparent", traceparent);
    if (baggage !== null) request.headers.set("baggage", baggage);
    return request;
  },
};
```

(If `openapi-fetch` doesn't re-export `Middleware` from the version in use, type the object inline; check `packages/ui/src/api/generated/client.ts` imports.)
- [ ] **Step 5:** In both panes' `appendSizeParams`, append trace params:

```ts
const { traceparent, baggage } = traceHeadersForRequest();
let result = `${url}${sep}cols=${cols}&rows=${rows}&resize_active=${resizeActive}&traceparent=${encodeURIComponent(traceparent)}`;
if (baggage !== null) result += `&baggage=${encodeURIComponent(baggage)}`;
return result;
```

(import `traceHeadersForRequest` in both panes; keep the existing `sep` logic.)
- [ ] **Step 6:** Full frontend checks from `frontend/`: `vp test run` (full), typecheck (`vp run frontend-package-typecheck` with heap bump), lint, svelte-autofixer on both panes. Expected: green modulo documented App.test/PierreFileDiff load flakes (verify isolation-pass if they fire).
- [ ] **Step 7:** Commit: `feat: propagate W3C trace context on API requests and terminal attach`.

### Task 7: collector make target, docs, end-to-end verification

**Files:**
- Modify: `Makefile` (new `otel-lgtm` target + `.PHONY`)
- Modify: `frontend/tests/profiling/README.md` ("Live tracing" section)

- [ ] **Step 1:** Makefile target:

```make
# Run the local all-in-one OTLP collector + Grafana/Tempo UI for
# middleman trace export. See frontend/tests/profiling/README.md.
otel-lgtm:
	docker run --rm -ti -p 3000:3000 -p 4317:4317 -p 4318:4318 grafana/otel-lgtm
```
- [ ] **Step 2:** README section (after "Correlating browser timings with Go pprof"): how to run `make otel-lgtm`, start middleman with `OTEL_TRACES_EXPORTER=otlp OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318 middleman serve`, find a switch's trace in Tempo (Grafana at `http://127.0.0.1:3000`) by the `traceId` in any `workspace-switch:*` measure detail or by the `workspace.id` span attribute; note propagation is always on and export is opt-in.
- [ ] **Step 3:** End-to-end verification (manual, requires docker): run the LGTM container, start the e2e server or `middleman serve` with the env vars, perform a workspace switch in the browser, query Tempo (`http://127.0.0.1:3000` or its API) for the trace ID printed by `performance.getEntriesByName("workspace-switch:first-paint")[0].detail.traceId`; confirm API spans named `GET /workspaces/{id}` etc. and a `terminal.attach` span. If docker is unavailable locally, state that explicitly in the final report instead of skipping silently — the Go wire tests still prove parenting/naming/attributes.
- [ ] **Step 4:** Run the profiling harness (`make profile-workspace-switch`) to confirm it still passes with propagation active.
- [ ] **Step 5:** Update `context/workspace-runtime-lifecycle.md`'s Switch-Timing Instrumentation section: one sentence that switch measures carry `detail.traceId` joining them to server-side OTel traces (opt-in export via `OTEL_TRACES_EXPORTER`).
- [ ] **Step 6:** Final validation sweep (full `vp test`, full `go test`, e2e chromium suite since frontend product code changed), close kata vjh3 with evidence, commit: `feat: document and verify opt-in OTel trace export`.

## Self-Review

- Spec coverage: frontend module (T5), client middleware + WS params + timing integration (T6), kit bump + Init (T1), otelhttp + Huma middleware + baggage attrs (T3), WS attach spans (T2+T4), LGTM + docs (T7), non-goals untouched. Testing section of spec maps to T2/T3/T5/T6 tests + T7 verification.
- Types consistent: `traceHeadersForRequest(): { traceparent: string; baggage: string | null }` used identically in T5/T6; `tracing.SetBaggageAttributes(ctx, span)` shared T2→T3/T4; `beginInteractionTrace(name, attrs) → string` consumed in T6.
- Known judgment calls left to the implementer at the marked spots: exact `newTestServer` helper name (T3), the precise End() seams in the two attach handlers (T4), and matching main.go's profiler error style (T1).
