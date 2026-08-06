import { describe, expect, it } from "vite-plus/test";

import { isProblem, problemCapability, problemConflictReason, problemRetryAfter, ProblemCodes } from "./problems";

describe("isProblem", () => {
  it("accepts a body with a known code", () => {
    expect(
      isProblem({
        code: ProblemCodes.unsupportedCapability,
        type: "about:blank",
        details: { capability: "merge_mutation" },
      }),
    ).toBe(true);
  });

  it("rejects null and non-objects", () => {
    expect(isProblem(null)).toBe(false);
    expect(isProblem(undefined)).toBe(false);
    expect(isProblem("error")).toBe(false);
    expect(isProblem(42)).toBe(false);
  });

  it("rejects objects without a code", () => {
    expect(isProblem({ detail: "missing code" })).toBe(false);
  });

  it("rejects objects whose code is unknown", () => {
    expect(isProblem({ code: "frobnicated" })).toBe(false);
  });
});

describe("problemCapability", () => {
  it("returns details.capability for an unsupportedCapability problem", () => {
    expect(
      problemCapability({
        code: ProblemCodes.unsupportedCapability,
        type: "about:blank",
        details: { capability: "merge_mutation" },
      }),
    ).toBe("merge_mutation");
  });

  it("returns undefined for other codes", () => {
    expect(
      problemCapability({
        code: ProblemCodes.badRequest,
        type: "about:blank",
      }),
    ).toBeUndefined();
  });

  it("returns undefined when details.capability is missing", () => {
    expect(
      problemCapability({
        code: ProblemCodes.unsupportedCapability,
        type: "about:blank",
      }),
    ).toBeUndefined();
  });
});

describe("problemConflictReason", () => {
  it("returns the stable conflict reasons from details.reason", () => {
    for (const reason of ["stale_state", "head_unknown", "not_open", "head_repo_unknown"] as const) {
      expect(
        problemConflictReason({
          code: ProblemCodes.conflict,
          type: "about:blank",
          details: { reason },
        }),
      ).toBe(reason);
    }
  });

  it("collapses missing and unrecognized reasons to the generic conflict", () => {
    expect(
      problemConflictReason({
        code: ProblemCodes.conflict,
        type: "about:blank",
      }),
    ).toBe("conflict");
    expect(
      problemConflictReason({
        code: ProblemCodes.conflict,
        type: "about:blank",
        details: { reason: "frobnicated" },
      }),
    ).toBe("conflict");
  });

  it("returns undefined for non-conflict codes", () => {
    expect(
      problemConflictReason({
        code: ProblemCodes.badRequest,
        type: "about:blank",
        details: { reason: "stale_state" },
      }),
    ).toBeUndefined();
  });
});

describe("problemRetryAfter", () => {
  it("parses an RFC 3339 retryAfter from a rateLimited problem", () => {
    const got = problemRetryAfter({
      code: ProblemCodes.rateLimited,
      type: "about:blank",
      details: { retryAfter: "2026-05-19T12:00:00Z" },
    });
    expect(got?.toISOString()).toBe("2026-05-19T12:00:00.000Z");
  });

  it("returns undefined for other codes", () => {
    expect(
      problemRetryAfter({
        code: ProblemCodes.badRequest,
        type: "about:blank",
      }),
    ).toBeUndefined();
  });

  it("returns undefined for malformed retryAfter", () => {
    expect(
      problemRetryAfter({
        code: ProblemCodes.rateLimited,
        type: "about:blank",
        details: { retryAfter: "not-a-date" },
      }),
    ).toBeUndefined();
  });
});
