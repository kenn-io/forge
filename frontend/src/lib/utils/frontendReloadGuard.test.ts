import { beforeEach, describe, expect, it } from "vitest";

import { prepareFrontendReload, retireFrontendReload } from "./frontendReloadGuard";

describe("frontend reload guard", () => {
  beforeEach(() => window.sessionStorage.clear());

  it("prevents a repeated reload while the source frontend is still loaded", () => {
    expect(prepareFrontendReload(window.sessionStorage, "/assets/index-a.js", "/assets/index-b.js")).toBe(true);
    expect(prepareFrontendReload(window.sessionStorage, "/assets/index-a.js", "/assets/index-b.js")).toBe(false);

    retireFrontendReload(window.sessionStorage, "/assets/index-a.js");

    expect(prepareFrontendReload(window.sessionStorage, "/assets/index-a.js", "/assets/index-b.js")).toBe(false);
  });

  it("retires the guard after the destination frontend loads", () => {
    expect(prepareFrontendReload(window.sessionStorage, "/assets/index-a.js", "/assets/index-b.js")).toBe(true);

    retireFrontendReload(window.sessionStorage, "/assets/index-b.js");

    expect(prepareFrontendReload(window.sessionStorage, "/assets/index-b.js", "/assets/index-a.js")).toBe(true);
  });

  it("retires the guard when the reload lands on a different frontend", () => {
    expect(prepareFrontendReload(window.sessionStorage, "/assets/index-a.js", "/assets/index-b.js")).toBe(true);

    retireFrontendReload(window.sessionStorage, "/assets/index-c.js");

    expect(prepareFrontendReload(window.sessionStorage, "/assets/index-c.js", "/assets/index-b.js")).toBe(true);
  });
});
