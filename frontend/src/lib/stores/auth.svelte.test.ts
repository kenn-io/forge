import { afterEach, describe, expect, it } from "vite-plus/test";
import { isAuthenticated, setAuthenticated, setUnauthenticated } from "./auth.svelte.js";

afterEach(() => setAuthenticated());

describe("auth store", () => {
  it("starts authenticated (optimistic)", () => {
    expect(isAuthenticated()).toBe(true);
  });

  it("setUnauthenticated flips to false", () => {
    setUnauthenticated();
    expect(isAuthenticated()).toBe(false);
  });

  it("setAuthenticated flips back to true", () => {
    setUnauthenticated();
    setAuthenticated();
    expect(isAuthenticated()).toBe(true);
  });
});
