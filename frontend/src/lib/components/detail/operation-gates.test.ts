import { afterEach, describe, expect, it, vi } from "vitest";
import type { OperationAvailability } from "../../api/types.js";
import { operationGate } from "./operation-gates.js";

afterEach(() => {
  vi.restoreAllMocks();
});

describe("operationGate", () => {
  it("formats rate-limit retry times in the user's local timezone", () => {
    const toLocaleTimeString = vi.spyOn(Date.prototype, "toLocaleTimeString").mockReturnValue("09:35");
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
    expect(toLocaleTimeString).toHaveBeenCalledWith(undefined, {
      hour: "2-digit",
      minute: "2-digit",
      hourCycle: "h23",
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
