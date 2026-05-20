import { describe, expect, it } from "vitest";
import { bucketForCheck } from "./ci-buckets.js";
import type { CICheck } from "../api/types.js";

const check = (partial: Partial<CICheck>): CICheck => ({
  name: "",
  status: "completed",
  conclusion: "",
  url: "",
  app: "",
  ...partial,
});

describe("bucketForCheck", () => {
  it("returns pending for active statuses", () => {
    for (const status of ["in_progress", "queued", "pending", "waiting"]) {
      expect(bucketForCheck(check({ status, conclusion: "" }))).toBe("pending");
    }
  });

  it("pending status takes precedence over conclusion", () => {
    expect(
      bucketForCheck(check({ status: "in_progress", conclusion: "failure" })),
    ).toBe("pending");
  });

  it("returns failed for known failure conclusions", () => {
    for (const conclusion of [
      "failure", "cancelled", "timed_out",
      "action_required", "stale", "startup_failure",
    ]) {
      expect(bucketForCheck(check({ conclusion }))).toBe("failed");
    }
  });

  it("returns passed for success", () => {
    expect(bucketForCheck(check({ conclusion: "success" }))).toBe("passed");
  });

  it("returns skipped for skipped/neutral", () => {
    expect(bucketForCheck(check({ conclusion: "skipped" }))).toBe("skipped");
    expect(bucketForCheck(check({ conclusion: "neutral" }))).toBe("skipped");
  });

  it("returns unknown for non-empty unrecognised conclusions", () => {
    expect(bucketForCheck(check({ conclusion: "weird_new_state" }))).toBe(
      "unknown",
    );
  });

  it("returns pending when status is non-active and conclusion is empty", () => {
    expect(bucketForCheck(check({ status: "", conclusion: "" }))).toBe("pending");
    expect(bucketForCheck(check({ status: "completed", conclusion: "" }))).toBe(
      "pending",
    );
  });

  it("trusts the conclusion when status is an unrecognised non-completed value", () => {
    // status='weird' is not 'completed' and not active. The classifier
    // falls through to conclusion-based bucketing, NOT Pending.
    expect(
      bucketForCheck(check({ status: "weird", conclusion: "success" })),
    ).toBe("passed");
    expect(
      bucketForCheck(check({ status: "weird", conclusion: "failure" })),
    ).toBe("failed");
    expect(
      bucketForCheck(check({ status: "weird", conclusion: "skipped" })),
    ).toBe("skipped");
  });

  it("returns pending for unrecognised status with empty conclusion (step 6 fallback)", () => {
    expect(bucketForCheck(check({ status: "weird", conclusion: "" }))).toBe(
      "pending",
    );
  });
});

import { bucketCIChecks } from "./ci-buckets.js";

describe("bucketCIChecks", () => {
  it("aggregates a mixed-state set into the right counts", () => {
    const result = bucketCIChecks([
      check({ status: "completed", conclusion: "failure" }),
      check({ status: "completed", conclusion: "success" }),
      check({ status: "completed", conclusion: "success" }),
      check({ status: "in_progress", conclusion: "" }),
      check({ status: "completed", conclusion: "skipped" }),
      check({ status: "completed", conclusion: "weird_state" }),
    ]);
    expect(result.failed.length).toBe(1);
    expect(result.pending.length).toBe(1);
    expect(result.passed.length).toBe(2);
    expect(result.skipped.length).toBe(1);
    expect(result.unknown.length).toBe(1);
    expect(result.all.length).toBe(6);
  });

  it("computes longestCompletedDurationSeconds across completed checks only", () => {
    const result = bucketCIChecks([
      check({ status: "completed", conclusion: "success", duration_seconds: 30 }),
      check({ status: "completed", conclusion: "success", duration_seconds: 120 }),
      check({ status: "in_progress", duration_seconds: 9999 }),
    ]);
    expect(result.longestCompletedDurationSeconds).toBe(120);
  });

  it("returns undefined longest when no completed check has duration", () => {
    const result = bucketCIChecks([
      check({ status: "in_progress", duration_seconds: 30 }),
      check({ status: "completed", conclusion: "success" }),
    ]);
    expect(result.longestCompletedDurationSeconds).toBeUndefined();
  });

  it("handles empty input", () => {
    const result = bucketCIChecks([]);
    expect(result.all.length).toBe(0);
    expect(result.longestCompletedDurationSeconds).toBeUndefined();
  });
});

import { parseCIChecks, safeDiagnosticText } from "./ci-buckets.js";

describe("parseCIChecks", () => {
  it("parses well-formed JSON into typed checks", () => {
    const json = JSON.stringify([
      { name: "build", status: "completed", conclusion: "success", url: "", app: "GH" },
    ]);
    const result = parseCIChecks(json);
    expect(result.error).toBeNull();
    expect(result.checks.length).toBe(1);
    expect(result.checks[0].name).toBe("build");
  });

  it("treats empty string as success with no checks (no error)", () => {
    expect(parseCIChecks("")).toEqual({ checks: [], error: null });
    expect(parseCIChecks("   ")).toEqual({ checks: [], error: null });
  });

  it("returns error for invalid JSON", () => {
    const result = parseCIChecks("{not json");
    expect(result.error).toBeInstanceOf(Error);
    expect(result.checks.length).toBe(0);
  });

  it("returns error when top-level value is not an array", () => {
    const result = parseCIChecks(JSON.stringify({ checks: [] }));
    expect(result.error).toBeInstanceOf(Error);
    expect(result.checks.length).toBe(0);
  });

  it("returns error when any element is non-object", () => {
    const result = parseCIChecks(JSON.stringify([{ name: "ok" }, "bad"]));
    expect(result.error).toBeInstanceOf(Error);
    expect(result.checks.length).toBe(0);
  });

  it("coerces missing fields to empty strings", () => {
    const result = parseCIChecks(JSON.stringify([{}]));
    expect(result.error).toBeNull();
    expect(result.checks[0].status).toBe("");
    expect(result.checks[0].conclusion).toBe("");
  });

  it("normalizes duration_seconds — drops NaN/negative/non-finite", () => {
    const result = parseCIChecks(
      JSON.stringify([
        { duration_seconds: 30 },
        { duration_seconds: -5 },
        { duration_seconds: "not a number" },
        { duration_seconds: null },
      ]),
    );
    expect(result.error).toBeNull();
    expect(result.checks[0].duration_seconds).toBe(30);
    expect(result.checks[1].duration_seconds).toBeUndefined();
    expect(result.checks[2].duration_seconds).toBeUndefined();
    expect(result.checks[3].duration_seconds).toBeUndefined();
  });

  it("safeDiagnosticText projects native JSON.parse errors to a single content-free string", () => {
    let parseErr: Error;
    try {
      JSON.parse(`{"x":"super_secret_sentinel_xyz",`); // truncated
      throw new Error("should have failed");
    } catch (e) {
      parseErr = e as Error;
    }
    const safe = safeDiagnosticText(parseErr);
    expect(safe).not.toContain("super_secret_sentinel_xyz");
    expect(safe).toBe("Malformed JSON");
  });

  it("safeDiagnosticText forwards locally-created CIChecksJSON shape errors intact", () => {
    expect(safeDiagnosticText(new Error("CIChecksJSON: payload is not an array")))
      .toBe("CIChecksJSON: payload is not an array");
  });
});
