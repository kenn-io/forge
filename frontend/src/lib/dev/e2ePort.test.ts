import { describe, expect, it } from "vite-plus/test";
import { e2eReuseExistingServer, parseE2EPort } from "./e2ePort";

describe("mock e2e port helpers", () => {
  it("parses explicit Playwright ports conservatively", () => {
    expect(parseE2EPort("4173")).toBe(4173);
    expect(parseE2EPort("0")).toBeNull();
    expect(parseE2EPort("65536")).toBeNull();
    expect(parseE2EPort("abc")).toBeNull();
    expect(parseE2EPort(undefined)).toBeNull();
  });

  it("only reuses an existing server after explicit opt-in", () => {
    expect(e2eReuseExistingServer({})).toBe(false);
    expect(
      e2eReuseExistingServer({
        PLAYWRIGHT_REUSE_EXISTING_SERVER: "0",
      }),
    ).toBe(false);
    expect(
      e2eReuseExistingServer({
        PLAYWRIGHT_REUSE_EXISTING_SERVER: "true",
      }),
    ).toBe(true);
    expect(
      e2eReuseExistingServer({
        PLAYWRIGHT_REUSE_EXISTING_SERVER: "yes",
      }),
    ).toBe(true);
  });
});
