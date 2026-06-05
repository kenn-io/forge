import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";

import WorkspaceHome from "./WorkspaceHome.svelte";

describe("WorkspaceHome", () => {
  afterEach(() => cleanup());

  it("renders launch targets and running sessions", async () => {
    const onLaunch = vi.fn();
    const onOpenSession = vi.fn();

    render(WorkspaceHome, {
      props: {
        workspace: {
          id: "ws-1",
          repo_owner: "acme",
          repo_name: "widget",
          item_number: 7,
          git_head_ref: "feature/workspace",
          worktree_path: "/tmp/widget",
          mr_title: "Improve workspace UX",
        },
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
            key: "shell",
            label: "Shell",
            kind: "shell",
            source: "system",
            available: true,
          },
          {
            key: "plain_shell",
            label: "Plain shell",
            kind: "plain_shell",
            source: "system",
            available: true,
          },
        ],
        sessions: [
          {
            key: "ws-1:codex",
            workspace_id: "ws-1",
            target_key: "codex",
            label: "Codex",
            kind: "agent",
            status: "running",
            created_at: "2026-04-25T00:00:00Z",
          },
          {
            key: "ws-1:shell",
            workspace_id: "ws-1",
            target_key: "plain_shell",
            label: "Shell",
            kind: "plain_shell",
            status: "running",
            created_at: "2026-04-25T00:00:01Z",
          },
        ],
        onLaunch,
        onOpenSession,
      },
    });

    expect(screen.getByText("Improve workspace UX")).toBeTruthy();
    expect(screen.getByText("/tmp/widget")).toBeTruthy();
    expect(
      (
        screen.getByRole("button", {
          name: "Codex",
        }) as HTMLButtonElement
      ).disabled,
    ).toBe(false);
    expect(
      (
        screen.getByRole("button", {
          name: "Missing",
        }) as HTMLButtonElement
      ).disabled,
    ).toBe(true);
    expect(screen.queryByRole("button", { name: "Plain shell" })).toBeNull();
    expect(screen.queryByRole("button", { name: "shell" })).toBeNull();

    await fireEvent.click(screen.getByRole("button", { name: "Codex" }));
    expect(onLaunch).toHaveBeenCalledWith("codex");
    await fireEvent.click(screen.getByRole("button", { name: "Shell" }));
    expect(onLaunch).toHaveBeenCalledWith("shell");

    await fireEvent.click(screen.getByRole("button", { name: /Codex\s+Running/ }));
    expect(onOpenSession).toHaveBeenCalledWith("ws-1:codex");
    await fireEvent.click(screen.getByRole("button", { name: /Shell\s+Running/ }));
    expect(onOpenSession).toHaveBeenCalledWith("ws-1:shell");
  });
});
