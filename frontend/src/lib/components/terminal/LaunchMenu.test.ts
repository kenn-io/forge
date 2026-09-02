import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";

import LaunchMenu from "./LaunchMenu.svelte";

describe("LaunchMenu", () => {
  afterEach(() => cleanup());

  it("hides disabled configured targets but keeps unavailable detected targets visible", async () => {
    const onLaunch = vi.fn();

    render(LaunchMenu, {
      props: {
        launchTargets: [
          {
            key: "codex",
            label: "Codex",
            kind: "agent",
            source: "builtin",
            available: true,
          },
          {
            key: "missing",
            label: "Missing",
            kind: "agent",
            source: "builtin",
            available: false,
            disabled_reason: "missing not found on PATH",
          },
          {
            key: "disabled_config",
            label: "Disabled config",
            kind: "agent",
            source: "config",
            available: false,
            disabled_reason: "disabled by config",
          },
          {
            key: "plain_shell",
            label: "Plain shell",
            kind: "plain_shell",
            source: "system",
            available: true,
          },
        ],
        onLaunch,
      },
    });

    await fireEvent.click(screen.getByRole("button", { name: "Launch" }));

    const codexOption = screen.getByRole("button", { name: /Codex/ });
    expect(codexOption.textContent?.trim()).toBe("Codex");
    expect((screen.getByRole("button", { name: /Missing/ }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.queryByRole("button", { name: /Disabled config/ })).toBeNull();
    expect(screen.getByRole("button", { name: /Shell/ }).textContent?.trim()).toBe("Shell");

    await fireEvent.click(codexOption);
    expect(onLaunch).toHaveBeenCalledWith("codex");
  });

  it("draws a harness glyph for agents whose key matches one and keeps the generic icon otherwise", async () => {
    render(LaunchMenu, {
      props: {
        launchTargets: [
          { key: "claude", label: "Claude", kind: "agent", source: "builtin", available: true },
          { key: "codex-review", label: "Review Agent", kind: "agent", source: "config", available: true },
          { key: "mystery", label: "mystery", kind: "agent", source: "config", available: true },
        ],
        onLaunch: vi.fn(),
      },
    });

    await fireEvent.click(screen.getByRole("button", { name: "Launch" }));

    const claude = screen.getByRole("button", { name: "Claude" });
    expect(claude.querySelector(".kit-harness-icon--claude svg")).not.toBeNull();
    expect(claude.querySelector(".launch-target-label")?.textContent).toBe("Claude");

    // The glyph comes from the key's leading segment; the configured label is untouched.
    const review = screen.getByRole("button", { name: "Review Agent" });
    expect(review.querySelector(".kit-harness-icon--openai")).not.toBeNull();
    expect(review.querySelector(".launch-target-label")?.textContent).toBe("Review Agent");

    const mystery = screen.getByRole("button", { name: "mystery" });
    expect(mystery.querySelector(".kit-harness-icon")).toBeNull();
    expect(mystery.querySelector(".launch-target-icon svg")).not.toBeNull();
  });
});
