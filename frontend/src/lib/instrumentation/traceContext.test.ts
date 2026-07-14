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
