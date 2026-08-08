import { describe, expect, it } from "vite-plus/test";

import type { ConfigRepo, Repo } from "../api/types.js";
import { buildMobileActivityRepoOptions } from "./mobileActivityRepoOptions.js";

const baseRepo = {
  provider: "github",
  owner: "acme",
  name: "widgets",
  repo_path: "acme/widgets",
  is_glob: false,
  matched_repo_count: 0,
};

function buildOptionsFromConfig(repos: ConfigRepo[]) {
  return buildMobileActivityRepoOptions(repos, []);
}

function catalogRepo(owner: string, name: string): Pick<Repo, "Platform" | "PlatformHost" | "Owner" | "Name"> {
  return {
    Platform: "github",
    PlatformHost: "github.com",
    Owner: owner,
    Name: name,
  };
}

describe("buildMobileActivityRepoOptions", () => {
  it("does not restore a renamed repo hidden under its current route", () => {
    const configured = [
      {
        ...baseRepo,
        platform_host: "github.com",
        name: "legacy-service",
        repo_path: "acme/legacy-service",
        matched_repo_count: 1,
      },
      {
        ...baseRepo,
        platform_host: "github.com",
        name: "archive-*",
        repo_path: "acme/archive-*",
        is_glob: true,
        hide_from_ui: true,
        matched_repo_count: 1,
      },
    ];

    expect(buildMobileActivityRepoOptions(configured, [])).toEqual([]);
  });

  it("combines the filtered catalog with only unresolved exact config rows", () => {
    const configured = [
      {
        ...baseRepo,
        platform_host: "github.com",
        name: "legacy-service",
        repo_path: "acme/legacy-service",
        matched_repo_count: 1,
      },
      {
        ...baseRepo,
        platform_host: "github.com",
        name: "not-imported-yet",
        repo_path: "acme/not-imported-yet",
        matched_repo_count: 0,
      },
    ];

    expect(buildMobileActivityRepoOptions(configured, [catalogRepo("acme", "service-next")])).toEqual([
      {
        value: "github|github.com/acme/not-imported-yet",
        label: "github/github.com/acme/not-imported-yet",
        triggerLabel: "acme/not-imported-yet",
      },
      {
        value: "github|github.com/acme/service-next",
        label: "github/github.com/acme/service-next",
        triggerLabel: "acme/service-next",
      },
    ]);
  });

  it("uses provider-qualified values when duplicate repo paths exist", () => {
    const options = buildOptionsFromConfig([
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
    const options = buildOptionsFromConfig([
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
    const options = buildOptionsFromConfig([
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
    const options = buildOptionsFromConfig([
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
    const options = buildOptionsFromConfig([
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
    const options = buildOptionsFromConfig([
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
    const options = buildOptionsFromConfig([
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
