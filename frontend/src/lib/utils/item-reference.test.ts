import { describe, expect, it } from "vite-plus/test";
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
