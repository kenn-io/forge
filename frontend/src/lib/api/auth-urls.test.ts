import { describe, expect, it } from "vite-plus/test";
import { logoutHref } from "./auth-urls.js";

describe("auth-urls", () => {
  it("logoutHref joins auth/logout onto the base", () => {
    expect(logoutHref("/middleman/")).toBe("/middleman/auth/logout");
    expect(logoutHref("/")).toBe("/auth/logout");
  });
});
