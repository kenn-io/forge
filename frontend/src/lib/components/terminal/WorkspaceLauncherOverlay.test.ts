import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { resetModalStack, getStackDepth } from "@kenn-forge/ui/stores/keyboard/modal-stack";

import WorkspaceLauncherOverlay from "./WorkspaceLauncherOverlay.svelte";

const workspace = {
  id: "ws-1",
  repo_owner: "acme",
  repo_name: "widget",
  item_number: 7,
  git_head_ref: "feature/launcher",
  worktree_path: "/tmp/widget",
  mr_title: "Improve workspace UX",
};

const launchTargets = [
  { key: "codex", label: "Codex", kind: "agent", source: "builtin", available: true },
  { key: "shell", label: "Shell", kind: "plain_shell", source: "builtin", available: true },
];

function renderOverlay(props: Record<string, unknown> = {}) {
  return render(WorkspaceLauncherOverlay, {
    props: {
      open: true,
      workspace,
      launchTargets,
      sessions: [],
      onClose: vi.fn(),
      onLaunch: vi.fn(),
      onOpenSession: vi.fn(),
      ...props,
    },
  });
}

describe("WorkspaceLauncherOverlay", () => {
  afterEach(() => {
    cleanup();
    resetModalStack();
  });

  it("offers the workspace's launch targets in a dialog", () => {
    const onLaunch = vi.fn();
    renderOverlay({ onLaunch });

    const dialog = screen.getByRole("dialog");
    expect(dialog.textContent).toContain("Launch a session");
    fireEvent.click(screen.getByRole("button", { name: /Codex/ }));

    expect(onLaunch).toHaveBeenCalledWith("codex");
  });

  it("holds a modal frame so global single-key shortcuts stay suppressed", () => {
    // The overlay covers the terminal, and j/k/Escape would otherwise still be
    // driving the list behind it.
    renderOverlay();

    expect(getStackDepth()).toBeGreaterThan(0);
  });

  it("renders nothing while closed", () => {
    renderOverlay({ open: false });

    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("reports a dismissal rather than closing itself", () => {
    const onClose = vi.fn();
    renderOverlay({ onClose });

    fireEvent.keyDown(window, { key: "Escape" });

    // The view owns the open state: it also opens this on launch failures and when
    // a workspace has no session, so a self-closing overlay would fight it.
    expect(onClose).toHaveBeenCalled();
    expect(screen.getByRole("dialog")).toBeTruthy();
  });
});
