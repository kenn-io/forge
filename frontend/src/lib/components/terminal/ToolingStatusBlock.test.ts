import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import ToolingStatusBlock from "./ToolingStatusBlock.svelte";

describe("ToolingStatusBlock", () => {
  beforeEach(() => {
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText: vi.fn().mockResolvedValue(undefined) },
    });
  });

  afterEach(() => cleanup());

  it("renders both git and gh rows when tooling is fully available", () => {
    render(ToolingStatusBlock, {
      props: {
        tooling: {
          git: { available: true, version: "2.45.0" },
          gh: {
            available: true,
            authenticated: true,
            user: "wesm",
            host: "github.com",
          },
        },
      },
    });

    expect(screen.getByText("git")).toBeTruthy();
    expect(screen.getByText("Available (2.45.0)")).toBeTruthy();
    expect(screen.getByText("gh")).toBeTruthy();
    expect(screen.getByText("Authenticated as wesm on github.com")).toBeTruthy();
  });

  it("renders the GitLab CLI row for GitLab providers", () => {
    render(ToolingStatusBlock, {
      props: {
        provider: "gitlab",
        tooling: {
          git: { available: true, version: "2.45.0" },
          gh: {
            available: true,
            authenticated: true,
            user: "wesm",
            host: "github.com",
          },
          glab: {
            available: true,
            authenticated: true,
            user: "wesm",
            host: "gitlab.com",
          },
        },
      },
    });

    expect(screen.getByText("git")).toBeTruthy();
    expect(screen.getByText("glab")).toBeTruthy();
    expect(screen.queryByText("gh")).toBeNull();
    expect(screen.getByText("Authenticated as wesm on gitlab.com")).toBeTruthy();
  });

  it("surfaces GitLab CLI recovery commands for GitLab providers", async () => {
    render(ToolingStatusBlock, {
      props: {
        provider: "gitlab",
        tooling: {
          git: { available: true },
          glab: { available: false, authenticated: false },
        },
      },
    });

    expect(screen.getByText("Not installed")).toBeTruthy();
    expect(screen.getByText("brew install glab")).toBeTruthy();
    await fireEvent.click(screen.getByLabelText("Copy glab install command"));
    expect(navigator.clipboard.writeText).toHaveBeenCalledWith("brew install glab");
  });

  it("surfaces a recovery command when gh is not authenticated", () => {
    render(ToolingStatusBlock, {
      props: {
        tooling: {
          git: { available: true },
          gh: { available: true, authenticated: false },
        },
      },
    });

    expect(screen.getByText("Not authenticated")).toBeTruthy();
    const code = screen.getByText("gh auth login");
    expect(code).toBeTruthy();
  });

  it("surfaces a brew install command when gh is missing", () => {
    render(ToolingStatusBlock, {
      props: {
        tooling: {
          git: { available: true },
          gh: { available: false, authenticated: false },
        },
      },
    });

    expect(screen.getByText("Not installed")).toBeTruthy();
    expect(screen.getByText("brew install gh")).toBeTruthy();
  });

  it("surfaces git recovery when git is missing", () => {
    render(ToolingStatusBlock, {
      props: {
        tooling: {
          git: { available: false },
          gh: { available: true, authenticated: true },
        },
      },
    });

    expect(screen.getByText("Not found on PATH")).toBeTruthy();
    expect(screen.getByText("xcode-select --install")).toBeTruthy();
  });

  it("copies the gh auth login command on button click", async () => {
    render(ToolingStatusBlock, {
      props: {
        tooling: {
          git: { available: true },
          gh: { available: true, authenticated: false },
        },
      },
    });

    const button = screen.getByLabelText("Copy auth command");
    await fireEvent.click(button);
    expect(navigator.clipboard.writeText).toHaveBeenCalledWith("gh auth login");
  });

  it("renders nothing when tooling is undefined and hideWhenUnknown is set", () => {
    const { container } = render(ToolingStatusBlock, {
      props: { tooling: undefined, hideWhenUnknown: true },
    });
    expect(container.querySelector(".tooling-block")).toBeNull();
  });

  it("renders the block when tooling is undefined and hideWhenUnknown is false", () => {
    render(ToolingStatusBlock, {
      props: { tooling: undefined },
    });
    // Both rows show their unknown indicator without recovery copy.
    expect(screen.getByText("git")).toBeTruthy();
    expect(screen.getByText("gh")).toBeTruthy();
    expect(screen.queryByText("brew install gh")).toBeNull();
    expect(screen.queryByText("gh auth login")).toBeNull();
  });
});
