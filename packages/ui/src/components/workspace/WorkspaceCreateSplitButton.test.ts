import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import type { LaunchTarget } from "../../api/types.js";
import WorkspaceCreateSplitButton from "./WorkspaceCreateSplitButton.svelte";

const targets: LaunchTarget[] = [
  {
    key: "codex",
    label: "Codex",
    kind: "agent",
    source: "builtin",
    command: ["codex"],
    available: true,
    disabled_reason: "",
  },
  {
    key: "review",
    label: "Review Agent",
    kind: "agent",
    source: "config",
    command: ["review-agent"],
    available: false,
    disabled_reason: "review-agent not found on PATH",
  },
  {
    key: "claude",
    label: "Claude",
    kind: "agent",
    source: "builtin",
    command: ["claude"],
    available: true,
    disabled_reason: "",
  },
  {
    key: "shell",
    label: "Shell",
    kind: "plain_shell",
    source: "system",
    command: [],
    available: true,
    disabled_reason: "",
  },
];

describe("WorkspaceCreateSplitButton", () => {
  afterEach(() => {
    cleanup();
  });

  it("keeps the primary action create-only", async () => {
    const onCreate = vi.fn();
    render(WorkspaceCreateSplitButton, {
      props: { label: "Create Workspace", launchTargets: targets, onCreate },
    });

    await fireEvent.click(screen.getByRole("button", { name: "Create Workspace" }));

    expect(onCreate).toHaveBeenCalledWith(undefined);
  });

  it("associates an optional description with only the primary action", async () => {
    render(WorkspaceCreateSplitButton, {
      props: {
        label: "Create Workspace",
        launchTargets: targets,
        descriptionId: "workspace-create-description",
        onCreate: vi.fn(),
      },
    });

    expect(screen.getByRole("button", { name: "Create Workspace" }).getAttribute("aria-describedby")).toBe(
      "workspace-create-description",
    );
    expect(
      screen.getByRole("button", { name: "Create Workspace options" }).getAttribute("aria-describedby"),
    ).toBeNull();

    await fireEvent.click(screen.getByRole("button", { name: "Create Workspace options" }));
    expect(screen.getByRole("menuitem", { name: "Codex" }).getAttribute("aria-describedby")).toBeNull();
  });

  it("offers only agents and passes the chosen target", async () => {
    const onCreate = vi.fn();
    render(WorkspaceCreateSplitButton, {
      props: { label: "Create Workspace", launchTargets: targets, onCreate },
    });

    await fireEvent.click(screen.getByRole("button", { name: "Create Workspace options" }));

    expect(screen.queryByRole("menuitem", { name: "Shell" })).toBeNull();

    await fireEvent.click(screen.getByRole("menuitem", { name: "Codex" }));

    expect(onCreate).toHaveBeenCalledWith("codex");
  });

  it("hides unavailable agents from the create-and-launch menu", async () => {
    render(WorkspaceCreateSplitButton, {
      props: {
        label: "Create Workspace",
        launchTargets: targets,
        onCreate: vi.fn(),
      },
    });

    await fireEvent.click(screen.getByRole("button", { name: "Create Workspace options" }));

    expect(screen.queryByRole("menuitem", { name: "Review Agent" })).toBeNull();
  });

  it("blocks both segments and exposes the blocking reason", () => {
    render(WorkspaceCreateSplitButton, {
      props: {
        label: "Create Workspace",
        launchTargets: targets,
        disabled: true,
        disabledReason: "Refresh details before creating a workspace.",
        onCreate: vi.fn(),
      },
    });

    const primary = screen.getByRole("button", { name: "Create Workspace" });
    const options = screen.getByRole("button", {
      name: "Create Workspace options",
    });

    expect((primary as HTMLButtonElement).disabled).toBe(true);
    expect((options as HTMLButtonElement).disabled).toBe(true);
    expect(primary.getAttribute("title")).toBe("Refresh details before creating a workspace.");
    expect(options.getAttribute("title")).toBe("Refresh details before creating a workspace.");
  });

  it("shows the busy label and blocks both segments", () => {
    render(WorkspaceCreateSplitButton, {
      props: {
        label: "Create Workspace",
        busyLabel: "Creating...",
        launchTargets: targets,
        busy: true,
        onCreate: vi.fn(),
      },
    });

    expect((screen.getByRole("button", { name: "Creating..." }) as HTMLButtonElement).disabled).toBe(true);
    expect(
      (
        screen.getByRole("button", {
          name: "Create Workspace options",
        }) as HTMLButtonElement
      ).disabled,
    ).toBe(true);
  });

  it("supports menu arrow, boundary, and keyboard activation", async () => {
    const onCreate = vi.fn();
    render(WorkspaceCreateSplitButton, {
      props: { label: "Create Workspace", launchTargets: targets, onCreate },
    });
    const trigger = screen.getByRole("button", {
      name: "Create Workspace options",
    });

    await fireEvent.keyDown(trigger, { key: "ArrowDown" });

    const codex = screen.getByRole("menuitem", { name: "Codex" });
    const claude = screen.getByRole("menuitem", { name: "Claude" });
    expect(document.activeElement).toBe(codex);

    await fireEvent.keyDown(codex, { key: "ArrowDown" });
    expect(document.activeElement).toBe(claude);
    await fireEvent.keyDown(claude, { key: "ArrowUp" });
    expect(document.activeElement).toBe(codex);
    await fireEvent.keyDown(codex, { key: "End" });
    expect(document.activeElement).toBe(claude);
    await fireEvent.keyDown(claude, { key: "Home" });
    expect(document.activeElement).toBe(codex);
    await fireEvent.keyDown(codex, { key: "ArrowDown" });
    expect(document.activeElement).toBe(claude);
    await fireEvent.keyDown(claude, { key: " " });

    expect(onCreate).toHaveBeenCalledWith("claude");

    await fireEvent.click(trigger);
    await fireEvent.keyDown(screen.getByRole("menuitem", { name: "Codex" }), { key: "Enter" });

    expect(onCreate).toHaveBeenCalledTimes(2);
    expect(onCreate).toHaveBeenLastCalledWith("codex");
  });

  it("dismisses on Escape, Tab, and outside press", async () => {
    render(WorkspaceCreateSplitButton, {
      props: {
        label: "Create Workspace",
        launchTargets: targets,
        onCreate: vi.fn(),
      },
    });
    const trigger = screen.getByRole("button", {
      name: "Create Workspace options",
    });

    await fireEvent.click(trigger);
    await fireEvent.keyDown(screen.getByRole("menuitem", { name: "Codex" }), { key: "Escape" });
    expect(screen.queryByRole("menu")).toBeNull();
    expect(document.activeElement).toBe(trigger);

    await fireEvent.click(trigger);
    await fireEvent.keyDown(screen.getByRole("menuitem", { name: "Codex" }), { key: "Tab" });
    expect(screen.queryByRole("menu")).toBeNull();

    await fireEvent.click(trigger);
    await fireEvent.pointerDown(document.body);
    expect(screen.queryByRole("menu")).toBeNull();
  });

  it("disables create-and-launch options when every agent is unavailable", async () => {
    const unavailableTargets = targets.map((target) =>
      target.kind === "agent" ? { ...target, available: false } : target,
    );
    render(WorkspaceCreateSplitButton, {
      props: {
        label: "Create Workspace",
        launchTargets: unavailableTargets,
        onCreate: vi.fn(),
      },
    });
    const trigger = screen.getByRole("button", {
      name: "Create Workspace options",
    });

    expect((trigger as HTMLButtonElement).disabled).toBe(true);
    await fireEvent.click(trigger);
    expect(screen.queryByRole("menu")).toBeNull();
  });
});
