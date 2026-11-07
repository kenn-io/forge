import { describe, expect, it } from "vite-plus/test";
import { githubAccount } from "./tooling-status.svelte.ts";

describe("githubAccount", () => {
  it.each([
    {
      name: "verified token with missing gh",
      gh: { available: false, authenticated: false, host: "ghe.example.com" },
      ready: true,
    },
    {
      name: "verified token with signed-in gh",
      gh: { available: true, authenticated: true, host: "ghe.example.com", user: "cli-maintainer" },
      ready: true,
    },
    {
      name: "unverified token with signed-in gh",
      gh: { available: true, authenticated: true, host: "github.com", user: "maintainer" },
      ready: false,
    },
  ])("selects the token for $name", ({ gh, ready }) => {
    expect(
      githubAccount({
        git: { available: true },
        gh,
        glab: { available: false, authenticated: false },
        github_token: { source: "TEST_GITHUB_TOKEN", ready },
      }),
    ).toEqual({ ready });
  });
});
