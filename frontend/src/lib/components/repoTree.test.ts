import { describe, expect, it } from "vitest";

import { buildRepoTree, type RepoTreeOption } from "./repoTree.js";

function opt(
  platformHost: string,
  repoPath: string,
  provider = "github",
): RepoTreeOption {
  const segments = repoPath.split("/");
  return {
    value: `${platformHost}/${repoPath}`,
    owner: segments.slice(0, -1).join("/"),
    name: segments[segments.length - 1] ?? repoPath,
    provider,
    platformHost,
  };
}

describe("buildRepoTree", () => {
  it("groups host -> owner -> repo and sorts each level", () => {
    const tree = buildRepoTree([
      opt("github.com", "acme/web"),
      opt("github.com", "acme/api"),
      opt("github.com", "widgets/sdk"),
    ]);

    expect(tree).toHaveLength(1);
    const host = tree[0]!;
    expect(host.kind).toBe("host");
    expect(host.id).toBe("github.com");
    expect(host.label).toBe("github.com");
    expect(host.provider).toBe("github");
    expect(host.children.map((o) => o.label)).toEqual(["acme", "widgets"]);

    const acme = host.children[0]!;
    expect(acme.id).toBe("github.com/acme");
    expect(acme.children.map((r) => r.label)).toEqual(["api", "web"]);
    expect(acme.children[0]!.value).toBe("github.com/acme/api");
    expect(acme.children[0]!.id).toBe("github.com/acme/api");
  });

  it("keeps GitLab nested groups as one slashed owner node", () => {
    const tree = buildRepoTree([
      opt("gitlab.com", "platform/frontend/web-ui", "gitlab"),
    ]);

    const host = tree[0]!;
    expect(host.children).toHaveLength(1);
    const owner = host.children[0]!;
    expect(owner.label).toBe("platform/frontend");
    expect(owner.id).toBe("gitlab.com/platform/frontend");
    expect(owner.children[0]!.label).toBe("web-ui");
    expect(owner.children[0]!.value).toBe("gitlab.com/platform/frontend/web-ui");
  });

  it("separates hosts and sorts them by label", () => {
    const tree = buildRepoTree([
      opt("gitlab.com", "g/x", "gitlab"),
      opt("github.com", "a/y"),
    ]);
    expect(tree.map((h) => h.label)).toEqual(["github.com", "gitlab.com"]);
  });

  it("uses the first option's provider when a host's providers disagree", () => {
    const tree = buildRepoTree([
      opt("ghe.example.com", "a/x", "github"),
      opt("ghe.example.com", "b/y", "gitlab"),
    ]);
    expect(tree[0]!.provider).toBe("github");
  });
});
