import { describe, expect, it } from "vite-plus/test";
import { loginHref, logoutHref } from "./auth-urls.js";

describe("auth-urls", () => {
  it("loginHref appends an encoded auth_token to a base ending in /", () => {
    expect(loginHref("/middleman/", " abc ".trim())).toBe("/middleman/?auth_token=abc");
    expect(loginHref("/", "a/b")).toBe("/?auth_token=a%2Fb");
  });

  it("logoutHref joins auth/logout onto the base", () => {
    expect(logoutHref("/middleman/")).toBe("/middleman/auth/logout");
    expect(logoutHref("/")).toBe("/auth/logout");
  });
});
