import { describe, expect, it } from "vitest";

import {
  isProblem,
  problemCapability,
  problemRetryAfter,
  ProblemCodes,
  readProblem,
} from "./problems";

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

describe("readProblem", () => {
  function jsonResponse(
    body: unknown,
    init: ResponseInit & { contentType?: string } = {},
  ): Response {
    const headers = new Headers(init.headers);
    headers.set("content-type", init.contentType ?? "application/problem+json");
    return new Response(JSON.stringify(body), {
      status: init.status ?? 409,
      statusText: init.statusText,
      headers,
    });
  }

  it("returns null for ok responses", async () => {
    const response = new Response("{}", { status: 200 });
    expect(await readProblem(response)).toBeNull();
  });

  it("returns null for non-json bodies", async () => {
    const response = new Response("oops", {
      status: 500,
      headers: { "content-type": "text/plain" },
    });
    expect(await readProblem(response)).toBeNull();
  });

  it("decodes a problem+json body", async () => {
    const response = jsonResponse({
      type: "about:blank",
      title: "Conflict",
      status: 409,
      detail: "Unsupported provider capability",
      code: ProblemCodes.unsupportedCapability,
      details: { capability: "merge_mutation", provider: "gitlab" },
    });
    const problem = await readProblem(response);
    expect(problem).not.toBeNull();
    expect(problem?.code).toBe(ProblemCodes.unsupportedCapability);
    expect(problem?.details?.["capability"]).toBe("merge_mutation");
  });

  it("returns null when the body is a different shape", async () => {
    const response = jsonResponse({ status: "ok" }, { status: 500 });
    expect(await readProblem(response)).toBeNull();
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
