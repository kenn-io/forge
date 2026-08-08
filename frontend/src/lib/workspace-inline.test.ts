import { describe, expect, it } from "vite-plus/test";
import { identityEquals, type WorkspaceItemIdentity } from "./workspace-inline.js";

const base: WorkspaceItemIdentity = {
  provider: "github",
  platformHost: "github.com",
  owner: "acme",
  name: "widgets",
  repoPath: "acme/widgets",
  number: 7,
  itemType: "pull",
};

describe("identityEquals", () => {
  it("treats provider aliases as their canonical provider", () => {
    // Route segments may carry gh/gl/fj while store data uses canonical
    // names; the same item must never compare as two identities.
    expect(identityEquals({ ...base, provider: "gh" }, base)).toBe(true);
    expect(identityEquals({ ...base, provider: "gl" }, base)).toBe(false);
  });

  it("distinguishes every other identity field", () => {
    expect(identityEquals(base, base)).toBe(true);
    expect(identityEquals(base, { ...base, number: 8 })).toBe(false);
    expect(identityEquals(base, { ...base, platformHost: "git.example.com" })).toBe(false);
    expect(identityEquals(base, { ...base, owner: "other" })).toBe(false);
  });

  it("treats item-type vocabularies as their canonical type", () => {
    // Detail components say "pull", the activity drawer says "pr", and
    // workspace envelopes say "pull_request" — all one identity.
    expect(identityEquals({ ...base, itemType: "pr" }, base)).toBe(true);
    expect(identityEquals({ ...base, itemType: "pull_request" }, base)).toBe(true);
  });

  it("treats an omitted host as the provider's default host", () => {
    // Activity URLs and provider-default routes may omit platform_host
    // while API payloads carry the concrete default host — one item.
    expect(identityEquals({ ...base, platformHost: undefined }, base)).toBe(true);
    expect(identityEquals({ ...base, platformHost: undefined }, { ...base, platformHost: "git.example.com" })).toBe(
      false,
    );
  });

  it("separates a PR from an issue sharing a repository and number", () => {
    // Without the item type, deleting PR #7 would tombstone Issue #7's
    // unrelated workspace in acme/widgets.
    expect(identityEquals(base, { ...base, itemType: "issue" })).toBe(false);
  });
});
