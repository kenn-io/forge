import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { Effect } from "effect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { makeAppRuntime, type OwnedAppRuntime } from "../../app/runtime.js";

const runtimeCapture = vi.hoisted(() => ({ current: undefined as OwnedAppRuntime | undefined }));

vi.mock("../../app/runtime-context.js", () => ({
  getAppRuntime: () => {
    const runtime = runtimeCapture.current;
    if (runtime === undefined) throw new Error("tooling status test runtime is not initialized");
    return runtime;
  },
}));

import ToolingStatusBlock from "./ToolingStatusBlock.svelte";

describe("ToolingStatusBlock", () => {
  beforeEach(() => {
    runtimeCapture.current = makeAppRuntime();
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText: vi.fn().mockResolvedValue(undefined) },
    });
  });

  afterEach(async () => {
    cleanup();
    if (runtimeCapture.current) await Effect.runPromise(runtimeCapture.current.disposeEffect);
    runtimeCapture.current = undefined;
    vi.useRealTimers();
  });

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
    expect(screen.getByLabelText("git available").classList.contains("kit-status-dot--idle")).toBe(true);
    expect(screen.getByLabelText("gh authenticated").classList.contains("kit-status-dot--idle")).toBe(true);
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
    expect(screen.getByRole("img", { name: "gh authentication required" })).toBeTruthy();
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
    expect(screen.getByLabelText("gh CLI missing").classList.contains("kit-status-dot--unclean")).toBe(true);
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

  it("keeps repeated copy feedback for the full latest interval", async () => {
    vi.useFakeTimers();
    render(ToolingStatusBlock, {
      props: {
        tooling: {
          git: { available: true },
          gh: { available: false, authenticated: false },
        },
      },
    });

    const button = screen.getByLabelText("gh CLI missing").closest("li")?.querySelector("button");
    expect(button).toBeTruthy();
    await fireEvent.click(button!);
    await vi.advanceTimersByTimeAsync(1_000);
    await fireEvent.click(button!);
    await vi.advanceTimersByTimeAsync(600);

    expect(button?.textContent).toBe("Copied");
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
    expect(screen.getByLabelText("git status unavailable").classList.contains("kit-status-dot--stale")).toBe(true);
    expect(screen.queryByText("brew install gh")).toBeNull();
    expect(screen.queryByText("gh auth login")).toBeNull();
  });
});
