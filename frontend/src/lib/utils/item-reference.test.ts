import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { buildCanonicalProviderItemURL, parseProviderItemURL } from "./item-reference.js";

const pull = {
  provider: "bitbucket",
  owner: "EX",
  name: "widgets",
  repoPath: "EX/widgets",
  number: 7,
  itemType: "pr" as const,
};

describe("Bitbucket item references", () => {
  it.each([
    [undefined, "https://bitbucket.org/EX/widgets/pull-requests/7"],
    ["bitbucket.org", "https://bitbucket.org/EX/widgets/pull-requests/7"],
    ["bitbucket.example.com", "https://bitbucket.example.com/projects/EX/repos/widgets/pull-requests/7"],
  ])("builds a pull request link for %s", (platformHost, expected) => {
    expect(buildCanonicalProviderItemURL({ ...pull, platformHost })).toBe(expected);
  });

  it.each([
    [undefined, "https://bitbucket.org/EX/widgets/pull-requests/7"],
    ["bitbucket.example.com", "https://bitbucket.example.com/projects/EX/repos/widgets/pull-requests/7"],
    ["bitbucket.example.com", "https://bitbucket.example.com/projects/EX/repos/widgets/pull-requests/7/overview"],
  ])("recognizes pasted pull request links on %s", (platformHost, url) => {
    expect(parseProviderItemURL(url, { provider: "bitbucket", platformHost })).toEqual({
      ...pull,
      platformHost,
      externalUrl: url,
    });
  });

  it("builds Cloud issue links without inventing Data Center issues", () => {
    expect(buildCanonicalProviderItemURL({ ...pull, itemType: "issue" })).toBe(
      "https://bitbucket.org/EX/widgets/issues/7",
    );
    expect(
      buildCanonicalProviderItemURL({ ...pull, itemType: "issue", platformHost: "bitbucket.example.com" }),
    ).toBeUndefined();
  });
});

describe("item reference hrefs", () => {
  const ref = {
    provider: "github",
    platformHost: "github.com",
    owner: "acme",
    name: "widgets",
    repoPath: "acme/widgets",
    number: 5,
    itemType: "pr" as const,
  };

  afterEach(() => {
    delete window.__BASE_PATH__;
    vi.resetModules();
  });

  async function importAtBasePath(basePath: string) {
    window.__BASE_PATH__ = basePath;
    vi.resetModules();
    return import("./item-reference.js");
  }

  it("prefixes links with the configured base path so new tabs and copied links reach Forge", async () => {
    const { buildItemReferenceLink, itemReferenceAnchorAttributes } = await importAtBasePath("/forge/");

    expect(buildItemReferenceLink(ref).href).toBe("/forge/pulls/github/acme/widgets/5");
    expect(itemReferenceAnchorAttributes(ref)).toContain('href="/forge/pulls/github/acme/widgets/5"');
  });

  it("leaves links unprefixed at the root base path", async () => {
    const { buildItemReferenceLink } = await importAtBasePath("/");

    expect(buildItemReferenceLink(ref).href).toBe("/pulls/github/acme/widgets/5");
  });
});
