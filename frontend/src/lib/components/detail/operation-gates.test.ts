import { afterEach, describe, expect, it, vi } from "vitest";
import type { OperationAvailability, ProviderCapabilities, RepoOperations } from "../../api/types.js";
import { displayedCapabilities, operationGate } from "./operation-gates.js";

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

describe("displayedCapabilities", () => {
  const fallback = { merge_mutation: true, comment_mutation: true } as ProviderCapabilities;
  const reported = { merge_mutation: false, comment_mutation: true } as ProviderCapabilities;

  it("keeps the reported capabilities when the provider lacks one", () => {
    const operations = {
      merge_pr: {
        available: false,
        code: "unsupported_capability",
        unavailable_reason: "Provider does not support merge_mutation",
      },
      add_comment: { available: true },
    } as RepoOperations;

    expect(displayedCapabilities({ capabilities: reported, operations }, fallback)).toBe(reported);
  });

  it("renders from the fallback while the provider host is unavailable", () => {
    const outage: OperationAvailability = {
      available: false,
      code: "provider_unavailable",
      unavailable_reason: "GitHub on github.com is unavailable: rate limit exceeded",
      retry_at: "2026-05-19T14:35:00Z",
    };
    const operations = { merge_pr: outage, add_comment: outage } as RepoOperations;

    expect(displayedCapabilities({ capabilities: reported, operations }, fallback)).toBe(fallback);
  });
});
