import { describe, expect, it } from "vite-plus/test";
import { createRepoLabelFormatter, repoIdentityKey } from "./repo-label.js";

const repos = [
  {
    provider: "github",
    platformHost: "github.com",
    owner: "acme",
    name: "widgets",
    repoPath: "acme/widgets",
  },
  {
    provider: "gitlab",
    platformHost: "gitlab.example.com",
    owner: "platform",
    name: "widgets",
    repoPath: "platform/widgets",
  },
  {
    provider: "github",
    platformHost: "ghe.example.com",
    owner: "acme",
    name: "widgets",
    repoPath: "acme/widgets",
  },
  {
    provider: "github",
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

  it("keeps same host owner/name repos on different providers distinguishable", () => {
    const sameHostOnDifferentProviders = [
      repos[0]!,
      {
        ...repos[0]!,
        provider: "gitea",
      },
    ];
    const formatter = createRepoLabelFormatter(sameHostOnDifferentProviders, {
      showOrgNames: true,
    });

    expect(formatter.format(sameHostOnDifferentProviders[0]!)).toBe("github/github.com/acme/widgets");
    expect(formatter.format(sameHostOnDifferentProviders[1]!)).toBe("gitea/github.com/acme/widgets");
  });

  it("keeps same host owner/name repos on different providers distinguishable when hiding orgs", () => {
    const sameHostOnDifferentProviders = [
      repos[0]!,
      {
        ...repos[0]!,
        provider: "gitea",
      },
    ];
    const formatter = createRepoLabelFormatter(sameHostOnDifferentProviders, {
      showOrgNames: false,
    });

    expect(formatter.format(sameHostOnDifferentProviders[0]!)).toBe("github/github.com/acme/widgets");
    expect(formatter.format(sameHostOnDifferentProviders[1]!)).toBe("gitea/github.com/acme/widgets");
  });

  it("keeps missing provider metadata distinguishable from a known provider", () => {
    const sameRepoWithMissingProvider = [
      repos[0]!,
      {
        ...repos[0]!,
        provider: "",
      },
    ];
    const formatter = createRepoLabelFormatter(sameRepoWithMissingProvider, {
      showOrgNames: false,
    });

    expect(formatter.format(sameRepoWithMissingProvider[0]!)).toBe("github/github.com/acme/widgets");
    expect(formatter.format(sameRepoWithMissingProvider[1]!)).toBe("github.com/acme/widgets");
  });

  it("uses the same provider-aware identity for shared grouping keys", () => {
    expect(repoIdentityKey(repos[0]!)).not.toBe(repoIdentityKey({ ...repos[0]!, provider: "gitea" }));
    expect(repoIdentityKey(repos[0]!)).not.toBe(repoIdentityKey(repos[2]!));
    expect(repoIdentityKey(repos[0]!)).toBe(repoIdentityKey({ ...repos[0]! }));
  });
});
