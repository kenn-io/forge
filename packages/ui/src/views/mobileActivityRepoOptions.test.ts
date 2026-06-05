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
  it("uses host-qualified trigger labels when duplicate repo paths exist", () => {
    const options = buildMobileActivityRepoOptions([
      { ...baseRepo, platform_host: "github.com" },
      { ...baseRepo, platform_host: "ghe.example.com" },
    ]);

    expect(options).toEqual([
      {
        value: "ghe.example.com/acme/widgets",
        label: "ghe.example.com/acme/widgets",
        triggerLabel: "ghe.example.com/acme/widgets",
      },
      {
        value: "github.com/acme/widgets",
        label: "github.com/acme/widgets",
        triggerLabel: "github.com/acme/widgets",
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
        value: "ghe.example.com/acme/api",
        label: "ghe.example.com/acme/api",
        triggerLabel: "acme/api",
      },
      {
        value: "github.com/acme/widgets",
        label: "github.com/acme/widgets",
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
      "ghe.example.com/acme/api",
      "github.com/acme/widgets",
      "github.com/zeta/widgets",
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
        value: "ghe.example.com/acme/widgets",
        label: "ghe.example.com/acme/widgets",
        triggerLabel: "acme/widgets",
      },
    ]);
  });
});
