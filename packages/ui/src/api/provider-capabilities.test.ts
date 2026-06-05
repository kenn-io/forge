import { describe, expect, it } from "vite-plus/test";

import { supportsLocked } from "./provider-capabilities.js";

describe("supportsLocked", () => {
  it("reports locked support for providers that expose lock state", () => {
    expect(supportsLocked("github", "github.com", "acme", "widgets")).toBe(true);
    expect(supportsLocked("forgejo", "codeberg.org", "forgejo", "forgejo")).toBe(true);
    expect(supportsLocked("gitea", "gitea.com", "gitea", "tea")).toBe(true);
  });

  it("does not report locked support for read-only GitLab data", () => {
    expect(supportsLocked("gitlab", "gitlab.com", "group", "project")).toBe(false);
  });
});
