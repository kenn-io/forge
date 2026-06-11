import { describe, expect, test } from "vite-plus/test";

import type { MessagesCapabilities } from "./types.js";
import { shouldShowMessagesMode } from "./visibility.js";

const baseFeatures = { threads_endpoint: false, mutations: false, attachments_download: false, sse_events: false };

function capabilities(input: Partial<MessagesCapabilities>): MessagesCapabilities {
  return {
    configured: false,
    ok: false,
    modes: [],
    features: baseFeatures,
    ...input,
  };
}

describe("shouldShowMessagesMode", () => {
  test("hides before capabilities load", () => {
    expect(shouldShowMessagesMode(null)).toBe(false);
  });

  test("hides when Messages is not configured", () => {
    expect(shouldShowMessagesMode(capabilities({ configured: false }))).toBe(false);
  });

  test.each([
    capabilities({ configured: true, ok: false, status: "misconfigured" }),
    capabilities({ configured: true, ok: false, status: "down" }),
    capabilities({ configured: true, ok: false, status: "unauthorized" }),
    capabilities({ configured: true, ok: true, status: "ok", modes: ["fts"] }),
  ])("shows configured states including degraded health: %o", (caps) => {
    expect(shouldShowMessagesMode(caps)).toBe(true);
  });
});
