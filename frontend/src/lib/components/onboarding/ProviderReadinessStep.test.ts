import { render, screen, within } from "@testing-library/svelte";
import { describe, expect, it, vi } from "vite-plus/test";
import ProviderReadinessStep from "./ProviderReadinessStep.svelte";

const callbacks = {
  retrying: false,
  retryError: null,
  onContinueGitHub: vi.fn(),
  onCheckAgain: vi.fn(),
  onOpenSettings: vi.fn(),
};

function githubRowText(): string {
  const [row] = within(screen.getByRole("list", { name: "Code forge readiness" })).getAllByRole("listitem");
  // The last child holds the title, status, and detail line after the provider icon.
  return (row?.lastElementChild?.textContent ?? "").replace(/\s+/g, " ").trim();
}

describe("ProviderReadinessStep", () => {
  it.each([
    {
      name: "missing gh with a token route",
      available: false,
      token: true,
      message: "Install and sign in to gh, or set KENN_FORGE_GITHUB_TOKEN and run kenn-forge daemon restart.",
    },
    {
      name: "signed-out gh with a token route",
      available: true,
      token: true,
      message:
        "gh is not authenticated for github.com. Sign in from a terminal, or set " +
        "KENN_FORGE_GITHUB_TOKEN and run kenn-forge daemon restart.",
    },
    {
      name: "missing gh without a token route",
      available: false,
      token: false,
      message: "gh is not installed. Install the GitHub CLI on this host, then check again.",
    },
    {
      name: "signed-out gh without a token route",
      available: true,
      token: false,
      message: "gh is not authenticated for github.com. Sign in from a terminal, then check again.",
    },
  ])("offers GitHub recovery for $name", ({ available, token, message }) => {
    const tooling = {
      git: { available: true, version: "2.50" },
      gh: { available, authenticated: false, host: "github.com" },
      glab: { available: false, authenticated: false },
      ...(token ? { github_token: { env: "KENN_FORGE_GITHUB_TOKEN", ready: false } } : {}),
    };
    render(ProviderReadinessStep, { props: { ...callbacks, tooling } });

    expect(githubRowText()).toBe(`GitHub Needs attention ${message}`);
  });

  it("reports an unverified token instead of falling back to a signed-in gh account", () => {
    const tooling = {
      git: { available: true, version: "2.50" },
      gh: { available: true, authenticated: true, host: "github.com", user: "maintainer" },
      glab: { available: false, authenticated: false },
      github_token: {
        env: "KENN_FORGE_GITHUB_TOKEN",
        source: "/tmp/forge-test/gh.token",
        ready: false,
      },
    };
    render(ProviderReadinessStep, { props: { ...callbacks, tooling } });

    expect(githubRowText()).toBe(
      "GitHub Needs attention Couldn't verify the token from /tmp/forge-test/gh.token on " +
        "github.com. If this keeps happening, check connectivity and the token's repository access. " +
        "If you change it, run kenn-forge daemon restart.",
    );
    expect(screen.queryByText("gh auth login")).toBeNull();
    expect(screen.queryByRole("button", { name: "Continue with GitHub" })).toBeNull();
  });

  it("identifies the verified token and host", () => {
    render(ProviderReadinessStep, {
      props: {
        ...callbacks,
        tooling: {
          git: { available: true },
          gh: { available: false, authenticated: false, host: "ghe.example.com" },
          glab: { available: false, authenticated: false },
          github_token: { env: "ACME_GHE_TOKEN", source: "ACME_GHE_TOKEN", ready: true },
        },
      },
    });

    expect(githubRowText()).toBe("GitHub Ready Token from ACME_GHE_TOKEN · ghe.example.com");
  });
});
