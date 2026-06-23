import { describe, expect, it } from "vite-plus/test";

import {
  buildRepoTree,
  collectLeafValues,
  nodeSelectionState,
  toggleSubtree,
  visibleRows,
  type RepoTreeOption,
  type VisibleRow,
} from "./repoTree.js";

function opt(platformHost: string, repoPath: string, provider = "github"): RepoTreeOption {
  const segments = repoPath.split("/");
  return {
    value: `${provider}|${platformHost}/${repoPath}`,
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
    expect(acme.children[0]!.value).toBe("github|github.com/acme/api");
    expect(acme.children[0]!.id).toBe("github|github.com/acme/api");
  });

  it("keeps GitLab nested groups as one slashed owner node", () => {
    const tree = buildRepoTree([opt("gitlab.com", "platform/frontend/web-ui", "gitlab")]);

    const host = tree[0]!;
    expect(host.children).toHaveLength(1);
    const owner = host.children[0]!;
    expect(owner.label).toBe("platform/frontend");
    expect(owner.id).toBe("gitlab.com/platform/frontend");
    expect(owner.children[0]!.label).toBe("web-ui");
    expect(owner.children[0]!.value).toBe("gitlab|gitlab.com/platform/frontend/web-ui");
  });

  it("separates hosts and sorts them by label", () => {
    const tree = buildRepoTree([opt("gitlab.com", "g/x", "gitlab"), opt("github.com", "a/y")]);
    expect(tree.map((h) => h.label)).toEqual(["github.com", "gitlab.com"]);
  });

  it("omits the host provider when a host's providers disagree", () => {
    const tree = buildRepoTree([opt("ghe.example.com", "a/x", "github"), opt("ghe.example.com", "b/y", "gitlab")]);
    expect(tree[0]!.provider).toBe("");
  });

  it("gives provider-qualified leaves visible provider labels", () => {
    const tree = buildRepoTree([
      {
        ...opt("github.com", "acme/widgets", "github"),
        value: "github|github.com/acme/widgets",
      },
      {
        ...opt("github.com", "acme/widgets", "gitea"),
        value: "gitea|github.com/acme/widgets",
      },
    ]);

    const acme = tree[0]!.children[0]!;
    expect(acme.children.map((repo) => repo.displayLabel)).toEqual(["gitea/widgets", "github/widgets"]);
  });

  it("returns an empty array for no options", () => {
    expect(buildRepoTree([])).toEqual([]);
  });
});

const neverCollapsed = () => false;

function labelsAtDepth(rows: VisibleRow[]): Array<[string, number]> {
  return rows.map((row) => [row.node.label, row.depth]);
}

describe("visibleRows", () => {
  it("omits the host node when there is only one host", () => {
    const tree = buildRepoTree([opt("github.com", "acme/api"), opt("github.com", "acme/web")]);
    const rows = visibleRows(tree, { isCollapsed: neverCollapsed });
    // owner at depth 0 (host omitted), two leaves at depth 1
    expect(labelsAtDepth(rows)).toEqual([
      ["acme", 0],
      ["api", 1],
      ["web", 1],
    ]);
  });

  it("shows host nodes at depth 0 when more than one host exists", () => {
    const tree = buildRepoTree([
      opt("github.com", "acme/api"),
      opt("github.com", "acme/web"),
      opt("gitlab.com", "g/x", "gitlab"),
    ]);
    const rows = visibleRows(tree, { isCollapsed: neverCollapsed });
    expect(rows[0]!.node.kind).toBe("host");
    expect(rows[0]!.depth).toBe(0);
    expect(rows.find((r) => r.node.label === "acme")?.depth).toBe(1);
  });

  it("flattens a single-repo owner even when multiple hosts exist", () => {
    const tree = buildRepoTree([
      opt("github.com", "acme/api"),
      opt("github.com", "acme/web"),
      opt("gitlab.com", "solo/only", "gitlab"),
    ]);
    const rows = visibleRows(tree, { isCollapsed: neverCollapsed });
    // gitlab.com host shows, its single-repo owner "solo" flattens to the "only"
    // leaf at the owner's depth (1); no "solo" owner row.
    const onlyRow = rows.find((r) => r.node.label === "only");
    expect(onlyRow).toBeTruthy();
    expect(onlyRow!.depth).toBe(1);
    expect(onlyRow!.hasChildren).toBe(false);
    expect(rows.some((r) => r.node.label === "solo")).toBe(false);
  });

  it("flattens a single-repo owner into one leaf row with no children", () => {
    const tree = buildRepoTree([
      opt("github.com", "acme/api"),
      opt("github.com", "acme/web"),
      opt("github.com", "solo/only"),
    ]);
    const rows = visibleRows(tree, { isCollapsed: neverCollapsed });
    const soloRow = rows.find((r) => r.node.label === "only");
    expect(soloRow).toBeTruthy();
    expect(soloRow!.depth).toBe(0); // at the owner's depth (single host)
    expect(soloRow!.hasChildren).toBe(false);
    // the "solo" owner node itself is not rendered
    expect(rows.some((r) => r.node.label === "solo")).toBe(false);
  });

  it("hides children of a collapsed node", () => {
    const tree = buildRepoTree([opt("github.com", "acme/api"), opt("github.com", "acme/web")]);
    const collapsed = (id: string) => id === "github.com/acme";
    const rows = visibleRows(tree, { isCollapsed: collapsed });
    expect(labelsAtDepth(rows)).toEqual([["acme", 0]]);
    expect(rows[0]!.expanded).toBe(false);
  });

  it("prunes non-matching repos and force-expands matches when filtering", () => {
    const tree = buildRepoTree([
      opt("github.com", "acme/api"),
      opt("github.com", "acme/web"),
      opt("github.com", "widgets/web-sdk"),
    ]);
    // collapse everything; filtering must override collapse
    const rows = visibleRows(tree, {
      isCollapsed: () => true,
      query: "web",
    });
    const labels = rows.map((r) => r.node.label);
    expect(labels).toContain("web");
    expect(labels).toContain("web-sdk");
    expect(labels).not.toContain("api");
  });

  it("matches provider-qualified leaves by visible and slash display labels", () => {
    const tree = buildRepoTree([
      {
        ...opt("github.com", "acme/widgets", "github"),
        value: "github|github.com/acme/widgets",
      },
      {
        ...opt("github.com", "acme/widgets", "gitea"),
        value: "gitea|github.com/acme/widgets",
      },
    ]);

    const visibleLabelRows = visibleRows(tree, {
      isCollapsed: () => true,
      query: "gitea/widgets",
    });
    expect(visibleLabelRows.map((row) => row.displayLabel ?? row.node.label)).toEqual(["acme", "gitea/widgets"]);

    const slashValueRows = visibleRows(tree, {
      isCollapsed: () => true,
      query: "gitea/github.com/acme/widgets",
    });
    expect(slashValueRows.map((row) => row.displayLabel ?? row.node.label)).toEqual(["acme", "gitea/widgets"]);
  });

  it("keeps a multi-repo owner as an owner row when a filter matches only one repo", () => {
    // acme genuinely has 3 repos; the query matches only "api". The owner must
    // stay a visible owner row (not collapse into a lone leaf), so its context
    // is preserved and same-named repos under other owners stay unambiguous.
    const tree = buildRepoTree([
      opt("github.com", "acme/api"),
      opt("github.com", "acme/web"),
      opt("github.com", "acme/infra"),
    ]);
    const rows = visibleRows(tree, {
      isCollapsed: () => false,
      query: "api",
    });
    const acme = rows.find((r) => r.node.label === "acme");
    expect(acme).toBeTruthy();
    expect(acme!.hasChildren).toBe(true);
    expect(acme!.node.kind).toBe("owner");
    // the single matching leaf is shown beneath the still-visible owner
    const api = rows.find((r) => r.node.label === "api");
    expect(api?.node.kind).toBe("repo");
    expect(rows.some((r) => r.node.label === "web")).toBe(false);
  });

  it("exposes the full subtree on a filtered owner row for selection", () => {
    // While filtering, the owner row must carry its ORIGINAL node (full child
    // set), not just the matching leaves, so tri-state and group-toggle apply
    // to the whole owner rather than only the visible matches.
    const tree = buildRepoTree([
      opt("github.com", "acme/api"),
      opt("github.com", "acme/web"),
      opt("github.com", "acme/infra"),
    ]);
    const rows = visibleRows(tree, {
      isCollapsed: () => false,
      query: "api",
    });
    const acme = rows.find((r) => r.node.label === "acme")!;
    // selection logic sees all three repos, not just the matching "api"
    expect(collectLeafValues(acme.node).sort()).toEqual([
      "github|github.com/acme/api",
      "github|github.com/acme/infra",
      "github|github.com/acme/web",
    ]);
    // with only "api" selected, the owner is partial (not "checked"), proving
    // tri-state reflects hidden siblings too
    expect(nodeSelectionState(acme.node, new Set(["github|github.com/acme/api"]))).toBe("partial");
    // toggling the owner cascades to the entire subtree, including hidden repos
    expect(toggleSubtree(acme.node, ["github|github.com/acme/api"]).sort()).toEqual([
      "github|github.com/acme/api",
      "github|github.com/acme/infra",
      "github|github.com/acme/web",
    ]);
  });

  it("labels flattened single-repo owners as owner/repo to keep them distinct", () => {
    // team-a and team-b each have one repo named "api". Flattened to leaves,
    // bare "api" rows would be indistinguishable; the displayLabel disambiguates
    // while the underlying node/value stays the leaf for selection.
    const tree = buildRepoTree([opt("github.com", "team-a/api"), opt("github.com", "team-b/api")]);
    const rows = visibleRows(tree, { isCollapsed: () => false });
    const labels = rows.map((r) => r.displayLabel ?? r.node.label);
    expect(labels).toContain("team-a/api");
    expect(labels).toContain("team-b/api");
    // node identity is still the leaf (value/id unchanged for selection)
    const teamA = rows.find((r) => r.displayLabel === "team-a/api")!;
    expect(teamA.node.kind).toBe("repo");
    expect((teamA.node as { value: string }).value).toBe("github|github.com/team-a/api");
  });

  it("still flattens a genuinely single-repo owner under a filter", () => {
    // solo really has one repo, so flattening it remains correct even mid-filter.
    const tree = buildRepoTree([
      opt("github.com", "acme/api"),
      opt("github.com", "acme/web"),
      opt("github.com", "solo/onlyrepo"),
    ]);
    const rows = visibleRows(tree, {
      isCollapsed: () => false,
      query: "only",
    });
    expect(rows.some((r) => r.node.label === "solo")).toBe(false);
    const leaf = rows.find((r) => r.node.label === "onlyrepo");
    expect(leaf?.hasChildren).toBe(false);
  });

  it("omits a collapsed host's owners in multi-host mode", () => {
    const tree = buildRepoTree([
      opt("github.com", "acme/api"),
      opt("github.com", "acme/web"),
      opt("gitlab.com", "g/x", "gitlab"),
    ]);
    const collapsed = (id: string) => id === "github.com";
    const rows = visibleRows(tree, { isCollapsed: collapsed });
    // github.com host row present but collapsed -> its owners/leaves omitted
    const githubHost = rows.find((r) => r.node.label === "github.com");
    expect(githubHost?.expanded).toBe(false);
    expect(rows.some((r) => r.node.label === "acme")).toBe(false);
    // gitlab.com still shows
    expect(rows.some((r) => r.node.label === "gitlab.com")).toBe(true);
  });

  it("drops an entire host when a filter matches nothing under it", () => {
    const tree = buildRepoTree([opt("github.com", "acme/web"), opt("gitlab.com", "g/api", "gitlab")]);
    const rows = visibleRows(tree, {
      isCollapsed: () => false,
      query: "web",
    });
    // only github.com/acme/web matches; gitlab.com host is dropped entirely,
    // and with one host remaining it auto-flattens (host row omitted)
    expect(rows.some((r) => r.node.label === "gitlab.com")).toBe(false);
    expect(rows.some((r) => r.node.label === "web")).toBe(true);
  });

  it("treats a whitespace-only query as no filter", () => {
    const tree = buildRepoTree([opt("github.com", "acme/api"), opt("github.com", "acme/web")]);
    const collapsed = (id: string) => id === "github.com/acme";
    const rows = visibleRows(tree, {
      isCollapsed: collapsed,
      query: "   ",
    });
    // whitespace trims to empty -> not filtering -> collapse is honored, leaves hidden
    expect(rows.some((r) => r.node.label === "api")).toBe(false);
    expect(rows.find((r) => r.node.label === "acme")?.expanded).toBe(false);
  });
});

describe("selection helpers", () => {
  const tree = buildRepoTree([
    opt("github.com", "acme/api"),
    opt("github.com", "acme/web"),
    opt("github.com", "acme/infra"),
  ]);
  const acme = tree[0]!.children[0]!;

  it("collects all descendant leaf values", () => {
    expect(collectLeafValues(acme).sort()).toEqual([
      "github|github.com/acme/api",
      "github|github.com/acme/infra",
      "github|github.com/acme/web",
    ]);
    expect(collectLeafValues(acme.children[0]!)).toEqual(["github|github.com/acme/api"]);
  });

  it("computes tri-state from the active set", () => {
    expect(nodeSelectionState(acme, new Set())).toBe("unchecked");
    expect(nodeSelectionState(acme, new Set(["github|github.com/acme/api"]))).toBe("partial");
    expect(
      nodeSelectionState(
        acme,
        new Set(["github|github.com/acme/api", "github|github.com/acme/web", "github|github.com/acme/infra"]),
      ),
    ).toBe("checked");
  });

  it("adds all subtree leaves when not fully checked", () => {
    expect(toggleSubtree(acme, ["github|github.com/acme/api"]).sort()).toEqual([
      "github|github.com/acme/api",
      "github|github.com/acme/infra",
      "github|github.com/acme/web",
    ]);
  });

  it("removes all subtree leaves when fully checked", () => {
    const all = ["github|github.com/acme/api", "github|github.com/acme/web", "github|github.com/acme/infra"];
    expect(toggleSubtree(acme, all)).toEqual([]);
  });

  it("toggles a single leaf without touching siblings", () => {
    expect(toggleSubtree(acme.children[0]!, ["github|github.com/acme/web"]).sort()).toEqual([
      "github|github.com/acme/api",
      "github|github.com/acme/web",
    ]);
  });
});
