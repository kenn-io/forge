import { afterEach, describe, expect, it } from "vitest";
import type { OperationAvailability } from "../../api/types.js";
import { operationGate } from "./operation-gates.js";

const originalTimeZone = process.env.TZ;

afterEach(() => {
  if (originalTimeZone === undefined) {
    delete process.env.TZ;
  } else {
    process.env.TZ = originalTimeZone;
  }
});

describe("operationGate", () => {
  it("formats rate-limit retry times in the user's local timezone", () => {
    process.env.TZ = "America/Chicago";
    const operation: OperationAvailability = {
      available: false,
      code: "rate_limited",
      unavailable_reason: "github.com rate-limited",
      retry_at: "2026-05-19T14:35:00Z",
    };

    expect(operationGate(operation)).toEqual({
      unavailable: true,
      reason: "github.com rate-limited; retry at 09:35",
    });
  });

  it("preserves non-rate-limit reasons", () => {
    const operation: OperationAvailability = {
      available: false,
      code: "missing_write_credential",
      unavailable_reason: "No user credential for writes on github.com",
    };

    expect(operationGate(operation)).toEqual({
      unavailable: true,
      reason: "No user credential for writes on github.com",
    });
  });
});
