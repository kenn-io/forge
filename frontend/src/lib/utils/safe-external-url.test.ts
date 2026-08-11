import { describe, expect, it } from "vite-plus/test";

import { isSafeExternalHTTPURL } from "./safe-external-url.js";

describe("isSafeExternalHTTPURL", () => {
  it.each([
    "javascript:alert(document.cookie)",
    "/issues/issue-1",
    "https:///issues/issue-1",
    "https://user:password@kata.example.test/issues/issue-1",
  ])("rejects unsafe external URL %s", (rawURL) => {
    expect(isSafeExternalHTTPURL(rawURL)).toBe(false);
  });

  it.each(["http://127.0.0.1:4222/issues/issue-1", "https://kata.example.test/issues/issue-1"])(
    "accepts external HTTP URL %s",
    (rawURL) => {
      expect(isSafeExternalHTTPURL(rawURL)).toBe(true);
    },
  );
});
