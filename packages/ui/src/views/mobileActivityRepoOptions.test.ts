import { describe, expect, it } from "vite-plus/test";

import { buildMobileActivityRepoOptions } from "./mobileActivityRepoOptions.js";

const baseRepo = {
  provider: "github",
  owner: "acme",
  name: "widgets",
  repo_path: "acme/widgets",
  is_glob: false,
  matched_repo_count: 0,
};

describe("buildMobileActivityRepoOptions", () => {
  it("uses provider-qualified values when duplicate repo paths exist", () => {
    const options = buildMobileActivityRepoOptions([
      { ...baseRepo, platform_host: "github.com" },
      { ...baseRepo, platform_host: "ghe.example.com" },
    ]);

    expect(options).toEqual([
      {
        value: "github|ghe.example.com/acme/widgets",
        label: "github/ghe.example.com/acme/widgets",
        triggerLabel: "github/ghe.example.com/acme/widgets",
      },
      {
        value: "github|github.com/acme/widgets",
        label: "github/github.com/acme/widgets",
        triggerLabel: "github/github.com/acme/widgets",
      },
    ]);
  });

  it("uses provider-qualified values when the same host and repo path exist on different providers", () => {
    const options = buildMobileActivityRepoOptions([
      { ...baseRepo, provider: "github", platform_host: "github.com" },
      { ...baseRepo, provider: "gitea", platform_host: "github.com" },
    ]);

    expect(options).toEqual([
      {
        value: "gitea|github.com/acme/widgets",
        label: "gitea/github.com/acme/widgets",
        triggerLabel: "gitea/github.com/acme/widgets",
      },
      {
        value: "github|github.com/acme/widgets",
        label: "github/github.com/acme/widgets",
        triggerLabel: "github/github.com/acme/widgets",
      },
    ]);
  });

  it("shortens trigger labels when repo paths are unique", () => {
    const options = buildMobileActivityRepoOptions([
      {
        ...baseRepo,
        platform_host: "github.com",
        repo_path: "acme/widgets",
      },
      {
        ...baseRepo,
        platform_host: "ghe.example.com",
        repo_path: "acme/api",
      },
    ]);

    expect(options).toEqual([
      {
        value: "github|ghe.example.com/acme/api",
        label: "github/ghe.example.com/acme/api",
        triggerLabel: "acme/api",
      },
      {
        value: "github|github.com/acme/widgets",
        label: "github/github.com/acme/widgets",
        triggerLabel: "acme/widgets",
      },
    ]);
  });

  it("sorts concrete repo options by label", () => {
    const options = buildMobileActivityRepoOptions([
      {
        ...baseRepo,
        platform_host: "github.com",
        repo_path: "zeta/widgets",
      },
      {
        ...baseRepo,
        platform_host: "github.com",
        repo_path: "acme/widgets",
      },
      {
        ...baseRepo,
        platform_host: "ghe.example.com",
        repo_path: "acme/api",
      },
    ]);

    expect(options.map((option) => option.label)).toEqual([
      "github/ghe.example.com/acme/api",
      "github/github.com/acme/widgets",
      "github/github.com/zeta/widgets",
    ]);
  });

  it("omits glob configuration rows because they are patterns, not selectable concrete repos", () => {
    const options = buildMobileActivityRepoOptions([
      {
        ...baseRepo,
        platform_host: "github.com",
        is_glob: true,
        repo_path: "acme/*",
      },
      {
        ...baseRepo,
        platform_host: "ghe.example.com",
        repo_path: "acme/widgets",
      },
    ]);

    expect(options).toEqual([
      {
        value: "github|ghe.example.com/acme/widgets",
        label: "github/ghe.example.com/acme/widgets",
        triggerLabel: "acme/widgets",
      },
    ]);
  });

  it("omits hidden exact repos and exact repos matched by hidden globs", () => {
    const options = buildMobileActivityRepoOptions([
      {
        ...baseRepo,
        platform_host: "github.com",
        name: "hidden",
        repo_path: "acme/hidden",
        hide_from_ui: true,
      },
      {
        ...baseRepo,
        platform_host: "github.com",
        name: "archive-*",
        repo_path: "acme/archive-*",
        is_glob: true,
        hide_from_ui: true,
      },
      {
        ...baseRepo,
        platform_host: "github.com",
        name: "archive-api",
        repo_path: "acme/archive-api",
        hide_from_ui: false,
      },
      {
        ...baseRepo,
        platform_host: "github.com",
        name: "visible",
        repo_path: "acme/visible",
        hide_from_ui: false,
      },
    ]);

    expect(options).toEqual([
      {
        value: "github|github.com/acme/visible",
        label: "github/github.com/acme/visible",
        triggerLabel: "acme/visible",
      },
    ]);
  });

  it("scopes hidden globs by provider and host", () => {
    const options = buildMobileActivityRepoOptions([
      {
        ...baseRepo,
        platform_host: "github.com",
        name: "archive-*",
        repo_path: "acme/archive-*",
        is_glob: true,
        hide_from_ui: true,
      },
      {
        ...baseRepo,
        provider: "gitea",
        platform_host: "github.com",
        name: "archive-api",
        repo_path: "acme/archive-api",
      },
      {
        ...baseRepo,
        platform_host: "ghe.example.com",
        name: "archive-api",
        repo_path: "acme/archive-api",
      },
    ]);

    expect(options.map((option) => option.value)).toEqual([
      "gitea|github.com/acme/archive-api",
      "github|ghe.example.com/acme/archive-api",
    ]);
  });
});
