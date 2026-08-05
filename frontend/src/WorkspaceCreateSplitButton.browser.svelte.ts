import { mount, unmount } from "svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import "./app.css";

import { setThemeMode } from "@kenn-io/kit-ui";
import { WorkspaceCreateSplitButton } from "@kenn-forge/ui";
import type { LaunchTarget } from "@kenn-forge/ui/api/types";

const launchTargets: LaunchTarget[] = [
  {
    key: "codex",
    label: "Codex",
    kind: "agent",
    source: "builtin",
    command: ["codex"],
    available: true,
    disabled_reason: "",
  },
];

function resolvedColor(value: string): string {
  const probe = document.createElement("span");
  probe.style.color = value;
  document.body.append(probe);
  const color = getComputedStyle(probe).color;
  probe.remove();
  return color;
}

function mountInScrollableModal(onCreate = vi.fn()): {
  app: Record<string, unknown>;
  overlay: HTMLElement;
  target: HTMLElement;
  onCreate: ReturnType<typeof vi.fn>;
} {
  const overlay = document.createElement("div");
  overlay.style.cssText = "position: fixed; inset: 0; z-index: 1000;";

  const panel = document.createElement("div");
  panel.className = "kit-modal-panel";
  panel.style.cssText = "position: fixed; left: 80px; top: 80px; width: 440px; background: var(--bg-surface);";

  const body = document.createElement("div");
  body.className = "kit-modal-body";
  body.style.cssText = "height: 210px; overflow-y: auto;";

  const target = document.createElement("div");
  target.style.cssText = "display: flex; justify-content: flex-end; margin-top: 165px; padding-right: 16px;";
  body.append(target);
  panel.append(body);
  overlay.append(panel);
  document.body.append(overlay);

  const app = mount(WorkspaceCreateSplitButton, {
    target,
    props: {
      label: "Create workspace",
      launchTargets,
      surface: "solid",
      onCreate,
    },
  });

  return { app, overlay, target, onCreate };
}

describe("workspace create split button in a modal", () => {
  let mounted: ReturnType<typeof mountInScrollableModal> | null = null;

  afterEach(() => {
    if (mounted) {
      unmount(mounted.app);
      mounted.overlay.remove();
      mounted = null;
    }
    setThemeMode("light");
  });

  it("renders the launch menu outside the scrollable modal body and keeps its items clickable", async () => {
    mounted = mountInScrollableModal();
    const trigger = mounted.target.querySelector<HTMLButtonElement>("button[aria-label='Create workspace options']");
    expect(trigger).not.toBeNull();

    trigger!.click();

    const menu = await vi.waitFor(() => {
      const element = document.querySelector<HTMLUListElement>("[role='menu'][aria-label='Create and launch']");
      expect(element).not.toBeNull();
      expect(element!.getBoundingClientRect().height).toBeGreaterThan(0);
      return element!;
    });
    expect(menu.parentElement).toBe(mounted.overlay.querySelector(".kit-modal-panel"));
    expect(menu.parentElement).not.toBe(mounted.overlay.querySelector(".kit-modal-body"));

    const item = menu.querySelector<HTMLButtonElement>("[role='menuitem']");
    expect(item).not.toBeNull();
    const rect = item!.getBoundingClientRect();
    const hit = document.elementFromPoint(rect.left + rect.width / 2, rect.top + rect.height / 2);
    expect(item === hit || item!.contains(hit)).toBe(true);

    item!.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true }));
    expect(document.body.contains(item)).toBe(true);
    item!.click();
    expect(mounted.onCreate).toHaveBeenCalledWith("codex");
  });

  it("styles both solid segments as one themed primary action", () => {
    setThemeMode("dark");
    mounted = mountInScrollableModal();

    const primary = mounted.target.querySelector<HTMLButtonElement>(".create-primary");
    const options = mounted.target.querySelector<HTMLButtonElement>(".create-options");
    expect(primary).not.toBeNull();
    expect(options).not.toBeNull();

    const primaryStyle = getComputedStyle(primary!);
    const optionsStyle = getComputedStyle(options!);
    const rootStyle = getComputedStyle(document.documentElement);
    const accent = resolvedColor(rootStyle.getPropertyValue("--accent-blue").trim());
    const foreground = resolvedColor(rootStyle.getPropertyValue("--bg-surface").trim());

    expect(primaryStyle.backgroundColor).toBe(accent);
    expect(optionsStyle.backgroundColor).toBe(accent);
    expect(primaryStyle.color).toBe(foreground);
    expect(optionsStyle.color).toBe(foreground);

    options!.focus();
    expect(getComputedStyle(options!).color).toBe(foreground);
  });
});
