import { cleanup, fireEvent, render, screen, within } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";

import WorkspaceLauncher from "./WorkspaceLauncher.svelte";

describe("WorkspaceLauncher", () => {
  afterEach(() => cleanup());

  it("renders launch targets and running sessions", async () => {
    const onLaunch = vi.fn();
    const onOpenSession = vi.fn();

    render(WorkspaceLauncher, {
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
            display_region: "workflow",
            created_at: "2026-04-25T00:00:00Z",
          },
          {
            key: "ws-1:shell",
            workspace_id: "ws-1",
            target_key: "plain_shell",
            label: "Shell",
            kind: "plain_shell",
            status: "running",
            display_region: "terminal",
            created_at: "2026-04-25T00:00:01Z",
          },
        ],
        onLaunch,
        onOpenSession,
      },
    });

    const codexLaunchButton = screen.getByRole("button", {
      name: "Codex",
    }) as HTMLButtonElement;
    expect(codexLaunchButton.disabled).toBe(false);
    expect(codexLaunchButton.textContent?.trim()).toBe("Codex");
    expect(codexLaunchButton.querySelector(".kit-harness-icon--openai")).not.toBeNull();
    expect(
      (
        screen.getByRole("button", {
          name: "Missing",
        }) as HTMLButtonElement
      ).disabled,
    ).toBe(true);
    expect(screen.queryByRole("button", { name: "Disabled config" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Plain shell" })).toBeNull();
    expect(screen.queryByRole("button", { name: "shell" })).toBeNull();

    const shellLaunchButton = screen.getByRole("button", {
      name: "Shell",
    }) as HTMLButtonElement;
    expect(shellLaunchButton.textContent?.trim()).toBe("Shell");
    expect(shellLaunchButton.querySelector(".kit-harness-icon")).toBeNull();
    expect(shellLaunchButton.querySelector(".launch-target-icon svg")).not.toBeNull();

    await fireEvent.click(codexLaunchButton);
    expect(onLaunch).toHaveBeenCalledWith("codex");
    await fireEvent.click(shellLaunchButton);
    expect(onLaunch).toHaveBeenCalledWith("plain_shell");

    await fireEvent.click(screen.getByRole("button", { name: /Codex\s+Running/ }));
    expect(onOpenSession).toHaveBeenCalledWith("ws-1:codex");
    await fireEvent.click(screen.getByRole("button", { name: /Shell\s+Running/ }));
    expect(onOpenSession).toHaveBeenCalledWith("ws-1:shell");
  });

  it("lists every stopped session as not running", () => {
    const stopped = (key: string, label: string, kind: "agent" | "plain_shell" | "acp") => ({
      key: `ws-1:${key}`,
      workspace_id: "ws-1",
      target_key: key,
      label,
      kind,
      status: "parked",
      display_region: "workflow",
      created_at: "2026-04-25T00:00:00Z",
    });
    render(WorkspaceLauncher, {
      props: {
        launchTargets: [],
        sessions: [
          stopped("codex", "Codex", "agent"),
          stopped("plain_shell", "Shell", "plain_shell"),
          stopped("chat", "Chat", "acp"),
        ],
        onLaunch: vi.fn(),
        onOpenSession: vi.fn(),
      },
    });

    for (const label of ["Codex", "Shell", "Chat"]) {
      expect(screen.getByRole("button", { name: new RegExp(`^${label}\\s+Not running$`) })).toBeTruthy();
    }
  });

  it("shows a launch on its card and announces it outside the cards", async () => {
    const onLaunch = vi.fn();
    const { rerender } = render(WorkspaceLauncher, {
      props: {
        launchTargets: [
          { key: "codex", label: "Codex", kind: "agent", source: "builtin", available: true },
          { key: "chat", label: "Chat agent", kind: "acp", source: "config", available: true },
        ],
        sessions: [],
        onLaunch,
      },
    });
    const card = screen.getByRole("button", { name: "Codex" }) as HTMLButtonElement;
    const status = screen.getByRole("status");
    expect(status.textContent?.trim()).toBe("");
    expect(card.querySelector(".kit-spinner")).toBeNull();

    await rerender({ launchingKey: "codex" });

    expect(screen.getByRole("status")).toBe(status);
    expect(status.textContent?.trim()).toBe("Launching Codex...");
    expect(card.contains(status)).toBe(false);
    expect(card.querySelector(".kit-spinner")).not.toBeNull();
    expect(card.textContent?.trim()).toBe("Codex");
    expect(card.disabled).toBe(false);
    expect(card.getAttribute("aria-disabled")).toBe("true");
    await fireEvent.click(card);
    expect(onLaunch).not.toHaveBeenCalled();

    await rerender({ launchingKey: null });

    expect(status.textContent?.trim()).toBe("");
    expect(card.querySelector(".kit-spinner")).toBeNull();
    expect(card.getAttribute("aria-disabled")).toBeNull();
    await fireEvent.click(card);
    expect(onLaunch).toHaveBeenCalledWith("codex");
  });

  it("groups ACP agents apart from terminal launch targets", async () => {
    const onLaunch = vi.fn();
    render(WorkspaceLauncher, {
      props: {
        launchTargets: [
          { key: "codex", label: "Codex", kind: "agent", source: "builtin", available: true },
          { key: "chat", label: "Chat agent", kind: "acp", source: "config", available: true },
          { key: "plain_shell", label: "Plain shell", kind: "plain_shell", source: "system", available: true },
        ],
        sessions: [],
        onLaunch,
      },
    });

    const terminal = within(screen.getByRole("group", { name: "Launch" }));
    const acp = within(screen.getByRole("group", { name: "ACP agents" }));
    expect(terminal.getAllByRole("button").map((button) => button.getAttribute("aria-label"))).toEqual([
      "Codex",
      "Shell",
    ]);
    expect(acp.getAllByRole("button").map((button) => button.getAttribute("aria-label"))).toEqual(["Chat agent"]);

    await fireEvent.click(acp.getByRole("button", { name: "Chat agent" }));
    expect(onLaunch).toHaveBeenCalledWith("chat");
  });

  it("opens and closes an ACP agent's resume list from its card", async () => {
    const onBrowseAcpSessions = vi.fn();
    const props = {
      launchTargets: [
        { key: "codex", label: "Codex", kind: "agent" as const, source: "builtin" as const, available: true },
        { key: "chat", label: "Chat agent", kind: "acp" as const, source: "config" as const, available: true },
      ],
      sessions: [],
      onBrowseAcpSessions,
    };
    const view = render(WorkspaceLauncher, { props });

    // Only ACP agents keep sessions a chat can resume.
    expect(screen.queryByRole("button", { name: "Resume a Codex session" })).toBeNull();
    await fireEvent.click(screen.getByRole("button", { name: "Resume a Chat agent session" }));
    expect(onBrowseAcpSessions).toHaveBeenLastCalledWith("chat");

    await view.rerender({ ...props, acpResume: { targetKey: "chat", status: "loading" } });
    const open = screen.getByRole("button", { name: "Resume a Chat agent session" });
    expect(open.getAttribute("aria-expanded")).toBe("true");
    await fireEvent.click(open);
    expect(onBrowseAcpSessions).toHaveBeenLastCalledWith(null);
  });

  it("resumes the session picked from an ACP agent's list", async () => {
    const onResumeAcp = vi.fn();
    render(WorkspaceLauncher, {
      props: {
        launchTargets: [{ key: "chat", label: "Chat agent", kind: "acp", source: "config", available: true }],
        sessions: [],
        onBrowseAcpSessions: vi.fn(),
        onResumeAcp,
        acpResume: {
          targetKey: "chat",
          status: "ready",
          sessions: [
            { session_id: "session-b", title: "Fix the parser", updated_at: "2026-11-21T09:30:00Z" },
            { session_id: "session-a" },
          ],
        },
      },
    });

    const list = within(screen.getByRole("group", { name: "Resume a Chat agent session" }));
    await fireEvent.click(list.getByRole("button", { name: /Untitled session/ }));
    expect(onResumeAcp).toHaveBeenCalledWith("chat", "session-a");
    await fireEvent.click(list.getByRole("button", { name: /Fix the parser/ }));
    expect(onResumeAcp).toHaveBeenLastCalledWith("chat", "session-b");
  });

  it("disables resuming while another launch runs", () => {
    render(WorkspaceLauncher, {
      props: {
        launchTargets: [{ key: "chat", label: "Chat agent", kind: "acp", source: "config", available: true }],
        sessions: [],
        launchingKey: "chat",
        onBrowseAcpSessions: vi.fn(),
        acpResume: { targetKey: "chat", status: "ready", sessions: [{ session_id: "session-a", title: "Earlier" }] },
      },
    });

    expect(screen.getByRole("button", { name: /Earlier/ }).hasAttribute("disabled")).toBe(true);
  });

  it("shows an empty or failed resume list", async () => {
    const props = {
      launchTargets: [
        { key: "chat", label: "Chat agent", kind: "acp" as const, source: "config" as const, available: true },
      ],
      sessions: [],
      onBrowseAcpSessions: vi.fn(),
    };
    const view = render(WorkspaceLauncher, {
      props: { ...props, acpResume: { targetKey: "chat", status: "ready", sessions: [] } },
    });
    expect(screen.getByText("No earlier sessions")).toBeTruthy();

    await view.rerender({
      ...props,
      acpResume: { targetKey: "chat", status: "failed", error: "this agent cannot list its sessions" },
    });
    expect(screen.getByRole("alert").textContent).toBe("this agent cannot list its sessions");
  });

  it("shows only the ACP section when every launch target is ACP", () => {
    render(WorkspaceLauncher, {
      props: {
        launchTargets: [{ key: "chat", label: "Chat agent", kind: "acp", source: "config", available: true }],
        sessions: [],
      },
    });

    expect(screen.queryByRole("group", { name: "Launch" })).toBeNull();
    expect(screen.getByRole("group", { name: "ACP agents" })).toBeTruthy();
  });

  it("runs configured quick actions and disables ones whose agent cannot launch", async () => {
    const onQuickAction = vi.fn();
    const review = { label: "Review", agent: "codex", prompt: "Review this change" };
    const { rerender } = render(WorkspaceLauncher, {
      props: {
        launchTargets: [
          { key: "codex", label: "Codex", kind: "agent", source: "builtin", available: true },
          {
            key: "missing",
            label: "Missing",
            kind: "agent",
            source: "builtin",
            available: false,
            disabled_reason: "missing not found on PATH",
          },
        ],
        sessions: [],
        quickActions: [
          review,
          { label: "Unavailable", agent: "missing", prompt: "Summarize" },
          { label: "Unknown", agent: "gone", prompt: "Summarize" },
        ],
        onQuickAction,
      },
    });

    const reviewButton = screen.getByRole("button", { name: "Review" }) as HTMLButtonElement;
    expect(reviewButton.disabled).toBe(false);
    expect(reviewButton.textContent).toContain("Codex");
    const unavailable = screen.getByRole("button", { name: "Unavailable" }) as HTMLButtonElement;
    expect(unavailable.disabled).toBe(true);
    expect(unavailable.title).toBe("missing not found on PATH");
    const unknown = screen.getByRole("button", { name: "Unknown" }) as HTMLButtonElement;
    expect(unknown.disabled).toBe(true);
    expect(unknown.title).toBe('Agent "gone" is not configured');

    await fireEvent.click(reviewButton);
    expect(onQuickAction).toHaveBeenCalledWith(review);
    await rerender({ quickActionBusy: true });
    expect(reviewButton.disabled).toBe(true);
    expect(screen.getByRole("button", { name: "Codex", exact: true }).hasAttribute("disabled")).toBe(false);
    await rerender({ quickActionBusy: false });
    expect(reviewButton.disabled).toBe(false);
  });

  it("lists quick actions by title, ignoring case", () => {
    const codex = { key: "codex", label: "Codex", kind: "agent", source: "builtin", available: true };
    render(WorkspaceLauncher, {
      props: {
        launchTargets: [codex],
        sessions: [],
        quickActions: [
          { label: "rebase", agent: "codex", prompt: "rebase" },
          { label: "Deep review", agent: "codex", prompt: "review" },
          { label: "apply fixes", agent: "codex", prompt: "fix" },
        ],
        onQuickAction: vi.fn(),
      },
    });

    const labels = Array.from(document.querySelectorAll(".quick-card"), (card) => card.getAttribute("aria-label"));
    expect(labels).toEqual(["apply fixes", "Deep review", "rebase"]);
  });

  it("omits the quick actions section when none are configured", () => {
    render(WorkspaceLauncher, {
      props: {
        launchTargets: [],
        sessions: [],
        quickActions: [],
        onQuickAction: vi.fn(),
      },
    });

    expect(screen.queryByText("Quick actions")).toBeNull();
  });
});
