import { describe, expect, it } from "vite-plus/test";
import { providerDisplayLabel } from "./provider-labels.js";

describe("providerDisplayLabel", () => {
  it("maps known provider keys to display labels", () => {
    expect(providerDisplayLabel("github")).toBe("GitHub");
    expect(providerDisplayLabel("gitlab")).toBe("GitLab");
    expect(providerDisplayLabel("forgejo")).toBe("Forgejo");
    expect(providerDisplayLabel("gitea")).toBe("Gitea");
  });

  it("canonicalizes shorthand and mixed-case keys", () => {
    expect(providerDisplayLabel("gh")).toBe("GitHub");
    expect(providerDisplayLabel("GL")).toBe("GitLab");
    expect(providerDisplayLabel("fj")).toBe("Forgejo");
  });

  it("falls back to the raw key for unknown providers", () => {
    expect(providerDisplayLabel("sourcehut")).toBe("sourcehut");
  });
});
