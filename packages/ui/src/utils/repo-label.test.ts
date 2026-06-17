import { describe, expect, it } from "vite-plus/test";
import { createRepoLabelFormatter } from "./repo-label.js";

const repos = [
  {
    platformHost: "github.com",
    owner: "acme",
    name: "widgets",
    repoPath: "acme/widgets",
  },
  {
    platformHost: "gitlab.example.com",
    owner: "platform",
    name: "widgets",
    repoPath: "platform/widgets",
  },
  {
    platformHost: "ghe.example.com",
    owner: "acme",
    name: "widgets",
    repoPath: "acme/widgets",
  },
  {
    platformHost: "github.com",
    owner: "acme",
    name: "api",
    repoPath: "acme/api",
  },
];

describe("repo labels", () => {
  it("uses owner/name by default and prefixes host only for host collisions", () => {
    const formatter = createRepoLabelFormatter(repos, {
      showOrgNames: true,
    });

    expect(formatter.format(repos[0]!)).toBe("github.com/acme/widgets");
    expect(formatter.format(repos[1]!)).toBe("platform/widgets");
    expect(formatter.format(repos[2]!)).toBe("ghe.example.com/acme/widgets");
    expect(formatter.format(repos[3]!)).toBe("acme/api");
  });

  it("hides org names only while repo names stay unambiguous", () => {
    const formatter = createRepoLabelFormatter(repos, {
      showOrgNames: false,
    });

    expect(formatter.format(repos[0]!)).toBe("github.com/acme/widgets");
    expect(formatter.format(repos[1]!)).toBe("platform/widgets");
    expect(formatter.format(repos[2]!)).toBe("ghe.example.com/acme/widgets");
    expect(formatter.format(repos[3]!)).toBe("api");
  });

  it("keeps repeated rows for the same repo collapsed to the repo name", () => {
    const sameRepo = [
      repos[0]!,
      {
        ...repos[0]!,
      },
    ];
    const formatter = createRepoLabelFormatter(sameRepo, {
      showOrgNames: false,
    });

    expect(formatter.format(sameRepo[0]!)).toBe("widgets");
    expect(formatter.format(sameRepo[1]!)).toBe("widgets");
  });

  it("keeps same owner/name repos on different hosts distinguishable when hiding orgs", () => {
    const samePathOnDifferentHosts = [repos[0]!, repos[2]!];
    const formatter = createRepoLabelFormatter(samePathOnDifferentHosts, {
      showOrgNames: false,
    });

    expect(formatter.format(samePathOnDifferentHosts[0]!)).toBe("github.com/acme/widgets");
    expect(formatter.format(samePathOnDifferentHosts[1]!)).toBe("ghe.example.com/acme/widgets");
  });
});
