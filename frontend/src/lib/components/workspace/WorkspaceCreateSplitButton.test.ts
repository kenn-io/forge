import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import type { LaunchTarget } from "../../api/types.js";
import WorkspaceCreateSplitButton from "./WorkspaceCreateSplitButtonRuntimeHarness.svelte";

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

  it("composes kit-ui buttons and keeps the primary action create-only", async () => {
    const onCreate = vi.fn();
    render(WorkspaceCreateSplitButton, {
      props: { label: "Create Workspace", launchTargets: targets, onCreate },
    });

    const primary = screen.getByRole("button", { name: "Create Workspace" });
    const options = screen.getByRole("button", { name: "Create Workspace options" });
    expect(primary.classList.contains("kit-button")).toBe(true);
    expect(options.classList.contains("kit-button")).toBe(true);

    await fireEvent.click(primary);

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

  it("offers only agents without a redundant visible heading and passes the chosen target", async () => {
    const onCreate = vi.fn();
    render(WorkspaceCreateSplitButton, {
      props: { label: "Create Workspace", launchTargets: targets, onCreate },
    });

    await fireEvent.click(screen.getByRole("button", { name: "Create Workspace options" }));

    expect(screen.queryByText("Create and launch")).toBeNull();
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

describe("WorkspaceCreateSplitButton quick actions", () => {
  afterEach(() => {
    cleanup();
  });

  const quickActions = [
    { label: "Rebase", agent: "codex", prompt: "rebase this pull request onto main" },
    { label: "Deep review", agent: "review", prompt: "review every assumption" },
    { label: "Ghost", agent: "missing", prompt: "never runs" },
  ];

  it("renders no quick-actions segment without configured actions", () => {
    render(WorkspaceCreateSplitButton, {
      props: { label: "Create Workspace", launchTargets: targets, onCreate: vi.fn(), onQuickAction: vi.fn() },
    });

    expect(screen.queryByRole("button", { name: "Quick actions" })).toBeNull();
  });

  it("lists quick actions, disables those whose agent cannot launch, and reports the pick", async () => {
    const onCreate = vi.fn();
    const onQuickAction = vi.fn();
    render(WorkspaceCreateSplitButton, {
      props: { label: "Create Workspace", launchTargets: targets, onCreate, onQuickAction, quickActions },
    });

    await fireEvent.click(screen.getByRole("button", { name: "Quick actions" }));

    const rebase = screen.getByRole("menuitem", { name: /Rebase/ });
    const deepReview = screen.getByRole("menuitem", { name: /Deep review/ }) as HTMLButtonElement;
    const ghost = screen.getByRole("menuitem", { name: /Ghost/ }) as HTMLButtonElement;
    expect(document.activeElement).toBe(rebase);
    expect(deepReview.disabled).toBe(true);
    expect(deepReview.getAttribute("title")).toBe("review-agent not found on PATH");
    expect(ghost.disabled).toBe(true);
    expect(ghost.getAttribute("title")).toBe('Agent "missing" is not configured');
    expect(screen.queryByRole("menuitem", { name: "Codex" })).toBeNull();

    await fireEvent.click(rebase);

    expect(onQuickAction).toHaveBeenCalledWith(quickActions[0]);
    expect(onCreate).not.toHaveBeenCalled();
    expect(screen.queryByRole("menu")).toBeNull();
  });

  it("keeps the agent menu and the quick-actions menu independent", async () => {
    render(WorkspaceCreateSplitButton, {
      props: {
        label: "Create Workspace",
        launchTargets: targets,
        onCreate: vi.fn(),
        onQuickAction: vi.fn(),
        quickActions,
      },
    });

    await fireEvent.click(screen.getByRole("button", { name: "Create Workspace options" }));
    expect(screen.getByRole("menu", { name: "Create and launch" })).toBeTruthy();
    expect(screen.queryByRole("menu", { name: "Quick actions" })).toBeNull();

    await fireEvent.click(screen.getByRole("button", { name: "Quick actions" }));
    expect(screen.getByRole("menu", { name: "Quick actions" })).toBeTruthy();
    expect(screen.queryByRole("menu", { name: "Create and launch" })).toBeNull();
  });

  it("blocks the quick-actions segment while busy or disabled", () => {
    render(WorkspaceCreateSplitButton, {
      props: {
        label: "Create Workspace",
        launchTargets: targets,
        busy: true,
        onCreate: vi.fn(),
        onQuickAction: vi.fn(),
        quickActions,
      },
    });

    expect((screen.getByRole("button", { name: "Quick actions" }) as HTMLButtonElement).disabled).toBe(true);
  });
});
