import { afterEach, describe, expect, it, vi } from "vitest";
import {
  warnOnMalformedCIChecksJSON,
  warnOnUnknownConclusions,
  __resetCIWarnings,
} from "./ci-buckets-warn.js";
import type { CICheck } from "../api/types.js";

const check = (conclusion: string): CICheck => ({
  name: "x",
  status: "completed",
  conclusion,
  url: "",
  app: "",
});

describe("warnOnUnknownConclusions", () => {
  afterEach(() => __resetCIWarnings());

  it("warns once per distinct conclusion", () => {
    const spy = vi.spyOn(console, "warn").mockImplementation(() => {});
    warnOnUnknownConclusions([check("foo"), check("foo"), check("bar")]);
    warnOnUnknownConclusions([check("foo"), check("bar"), check("baz")]);
    expect(spy).toHaveBeenCalledTimes(3); // foo, bar, baz
    spy.mockRestore();
  });

  it("does nothing for empty input", () => {
    const spy = vi.spyOn(console, "warn").mockImplementation(() => {});
    warnOnUnknownConclusions([]);
    expect(spy).not.toHaveBeenCalled();
    spy.mockRestore();
  });

  it("includes PR identifier in the log message when context is provided", () => {
    const spy = vi.spyOn(console, "warn").mockImplementation(() => {});
    warnOnUnknownConclusions([check("foo")], { repo: "a/b", number: 7 });
    expect(spy.mock.calls[0]?.[0] as string).toContain("a/b#7");
    spy.mockRestore();
  });

  it("truncates pathologically long conclusion values when warning and dedupes by the truncated form", () => {
    const spy = vi.spyOn(console, "warn").mockImplementation(() => {});
    const long = "X".repeat(2000);
    warnOnUnknownConclusions([check(long)]);
    const message = spy.mock.calls[0]?.[0] as string;
    // The raw 2000-char value must not appear in the log line.
    expect(message).not.toContain("X".repeat(200));
    // The truncated form ends with the truncation marker.
    expect(message).toContain("…");
    // A second call with a string that shares the same truncated prefix
    // is treated as a duplicate (best-effort dedupe — distinct providers
    // that all overrun the cap with the same prefix get one warning).
    warnOnUnknownConclusions([check("X".repeat(2000) + "_different_tail")]);
    expect(spy).toHaveBeenCalledTimes(1);
    spy.mockRestore();
  });
});

describe("warnOnMalformedCIChecksJSON", () => {
  afterEach(() => __resetCIWarnings());

  it("warns once per (context+payload) pair", () => {
    const spy = vi.spyOn(console, "warn").mockImplementation(() => {});
    const raw = "{not json";
    const err = new Error("parse fail");
    warnOnMalformedCIChecksJSON(raw, err, { repo: "a/b", number: 1 });
    warnOnMalformedCIChecksJSON(raw, err, { repo: "a/b", number: 1 });
    warnOnMalformedCIChecksJSON(raw, err, { repo: "a/b", number: 2 });
    expect(spy).toHaveBeenCalledTimes(2);
    spy.mockRestore();
  });

  it("logs only metadata and category in production mode — never the raw error.message or input content", () => {
    // Force production: import.meta.env.DEV === false. vi.stubEnv mutates
    // the live env object that ci-buckets-warn.ts reads at call time.
    vi.stubEnv("DEV", false);
    const spy = vi.spyOn(console, "warn").mockImplementation(() => {});
    // Critically: trigger a REAL JSON.parse so error.message contains a
    // preview of the malformed input. Native V8/JSC messages embed input
    // fragments like "Unexpected token X in JSON at position N" or
    // "Unexpected end of JSON input near ...sentinel...". The production
    // logger must NOT forward error.message.
    const sentinel = "supersecret_sentinel_xyz";
    const raw = `{"bad":"${sentinel}",`; // truncated, will fail JSON.parse
    let parseErr: Error;
    try {
      JSON.parse(raw);
      throw new Error("parse should have failed");
    } catch (e) {
      parseErr = e as Error;
    }
    // Sanity: confirm the sentinel actually appears somewhere in the
    // real parse error so we're testing the leak path, not a no-op.
    // (Some engines may not embed it; if so, swap the raw payload until
    //  the engine's message includes a preview.)
    // expect(parseErr.message).toContain(sentinel); // intentionally
    // commented — we don't want the test to fail on engine variation,
    // just to assert the production logger doesn't leak even when the
    // sentinel IS present.
    warnOnMalformedCIChecksJSON(raw, parseErr);
    const message = spy.mock.calls[0]?.[0] as string;
    expect(message).not.toContain(sentinel);
    expect(message).not.toContain(parseErr.message);
    expect(message).not.toContain("Preview:");
    expect(message).toContain("Malformed JSON");
    expect(message).toMatch(/length=\d+/);
    spy.mockRestore();
    vi.unstubAllEnvs();
  });

  it("forwards locally-created shape error messages in production (no leak risk)", () => {
    // Errors created by parseCIChecks have a stable "CIChecksJSON: ..."
    // prefix — content-free and safe to log. Confirm they pass through
    // intact rather than collapsing to a generic category.
    vi.stubEnv("DEV", false);
    const spy = vi.spyOn(console, "warn").mockImplementation(() => {});
    warnOnMalformedCIChecksJSON("[1, 2]", new Error("CIChecksJSON: element 0 is not an object"));
    const message = spy.mock.calls[0]?.[0] as string;
    expect(message).toContain("CIChecksJSON: element 0 is not an object");
    spy.mockRestore();
    vi.unstubAllEnvs();
  });

  it("includes raw error.message and a 64-char Preview clause in dev mode and caps the preview at 64 chars", () => {
    // Force dev: import.meta.env.DEV === true.
    vi.stubEnv("DEV", true);
    const spy = vi.spyOn(console, "warn").mockImplementation(() => {});
    const raw = "A".repeat(200); // well past the 64-char cap
    warnOnMalformedCIChecksJSON(raw, new Error("bad"));
    const message = spy.mock.calls[0]?.[0] as string;
    expect(message).toContain("bad"); // raw error.message present in dev
    expect(message).toContain("Preview: ");
    // The Preview clause shows exactly 64 'A's plus the ellipsis marker.
    expect(message).toContain(`Preview: ${"A".repeat(64)}…`);
    expect(message).not.toContain("A".repeat(65));
    // Metadata clause must still be present regardless of mode.
    expect(message).toMatch(/length=200/);
    spy.mockRestore();
    vi.unstubAllEnvs();
  });

  it("includes PR identifier when context is provided", () => {
    const spy = vi.spyOn(console, "warn").mockImplementation(() => {});
    warnOnMalformedCIChecksJSON("{}", new Error("bad"), { repo: "x/y", number: 42 });
    expect((spy.mock.calls[0]?.[0] as string)).toContain("x/y#42");
    spy.mockRestore();
  });
});
