import { describe, expect, it } from "vite-plus/test";

import { effectiveActivity } from "./effective-activity.js";

describe("effectiveActivity", () => {
  it("uses newer workspace activity without changing the provider timestamp", () => {
    const providerAt = "2026-08-09T10:00:00Z";

    expect(effectiveActivity(providerAt, "2026-08-09T11:00:00Z")).toEqual({
      at: "2026-08-09T11:00:00Z",
      fromWorkspace: true,
    });
    expect(providerAt).toBe("2026-08-09T10:00:00Z");
  });

  it("keeps provider activity when workspace activity is absent or older", () => {
    expect(effectiveActivity("2026-08-09T10:00:00Z")).toEqual({
      at: "2026-08-09T10:00:00Z",
      fromWorkspace: false,
    });
    expect(effectiveActivity("2026-08-09T10:00:00Z", "2026-08-09T09:00:00Z")).toEqual({
      at: "2026-08-09T10:00:00Z",
      fromWorkspace: false,
    });
  });
});
