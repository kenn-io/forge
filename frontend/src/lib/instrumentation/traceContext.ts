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

// Begins a new interaction trace, superseding any live interaction.
// Returns the minted trace id so callers can carry it (e.g. into
// workspace-switch measure details) and later end this exact trace
// with endInteractionTrace.
export function beginInteractionTrace(name: string, attrs: Record<string, string>): string {
  const traceId = randomHex(16);
  currentInteraction = {
    traceId,
    baggage: encodeBaggage({ interaction: name, ...attrs }),
  };
  return traceId;
}

// Ends the live interaction. With a traceId (from beginInteractionTrace)
// only that exact interaction is ended — a caller holding a superseded
// trace id cannot end a newer interaction someone else began.
export function endInteractionTrace(traceId?: string): void {
  if (traceId !== undefined && currentInteraction?.traceId !== traceId) return;
  currentInteraction = null;
}

export function currentInteractionTraceId(): string | null {
  return currentInteraction?.traceId ?? null;
}

// Trace headers for one request: interaction-parented (same trace id,
// fresh span id, interaction baggage) when an interaction is live,
// otherwise a fresh single-request trace with no baggage.
export function traceHeadersForRequest(): { traceparent: string; baggage: string | null } {
  const traceId = currentInteraction?.traceId ?? randomHex(16);
  return {
    traceparent: `00-${traceId}-${randomHex(8)}-01`,
    baggage: currentInteraction?.baggage ?? null,
  };
}
