import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { resetModalStack, getStackDepth } from "../../stores/keyboard/modal-stack.svelte.js";

import WorkspaceLauncherOverlay from "./WorkspaceLauncherOverlay.svelte";
import AppRuntimeHarness from "../../../test/AppRuntimeHarness.svelte";

const launchTargets = [
  { key: "codex", label: "Codex", kind: "agent", source: "builtin", available: true },
  { key: "shell", label: "Shell", kind: "plain_shell", source: "builtin", available: true },
];

function renderOverlay(props: Record<string, unknown> = {}) {
  return render(AppRuntimeHarness, {
    props: {
      component: WorkspaceLauncherOverlay,
      open: true,
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

  it("releases keyboard ownership when focus leaves the launcher", async () => {
    renderOverlay();
    expect(getStackDepth()).toBeGreaterThan(0);
    const outside = document.createElement("button");
    document.body.append(outside);
    try {
      outside.focus();
      await waitFor(() => expect(getStackDepth()).toBe(0));
      expect(document.activeElement).toBe(outside);
      expect(screen.getByRole("dialog")).toBeTruthy();
    } finally {
      outside.remove();
    }
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
