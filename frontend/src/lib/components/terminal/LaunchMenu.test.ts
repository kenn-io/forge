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

    expect(screen.getByRole("button", { name: /Codex/ })).toBeTruthy();
    expect((screen.getByRole("button", { name: /Missing/ }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.queryByRole("button", { name: /Disabled config/ })).toBeNull();
    expect(screen.getByRole("button", { name: /Shell/ })).toBeTruthy();

    await fireEvent.click(screen.getByRole("button", { name: /Codex/ }));
    expect(onLaunch).toHaveBeenCalledWith("codex");
  });
});
