import { mount, unmount } from "svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { page, userEvent } from "vite-plus/test/browser";
import "@kenn-io/kit-ui/theme.css";

import ActivityFilters from "./lib/components/ActivityFilters.svelte";

describe("Activity Filters popover", () => {
  let host: HTMLElement | null = null;
  let app: Record<string, unknown> | null = null;

  afterEach(() => {
    if (app) unmount(app);
    host?.remove();
    app = null;
    host = null;
  });

  it("escapes an overflow-hidden container and remains clickable", async () => {
    await page.viewport(900, 700);
    host = document.createElement("div");
    host.style.cssText = [
      "position: fixed",
      "left: 80px",
      "top: 120px",
      "width: 280px",
      "height: 28px",
      "overflow: hidden",
      "container-type: inline-size",
    ].join(";");
    document.body.append(host);

    app = mount(ActivityFilters, {
      target: host,
      props: {
        author: "",
        authorOptions: [{ name: "alice", label: "Alice" }],
        sections: [],
        onAuthorSelect: vi.fn(),
      },
    });

    const trigger = host.querySelector<HTMLButtonElement>("button[aria-label='Filters']");
    expect(trigger).not.toBeNull();
    trigger!.click();

    const panel = await vi.waitFor(() => {
      const candidate = document.querySelector<HTMLElement>("[aria-label='Activity filters']");
      expect(candidate).not.toBeNull();
      expect(candidate!.getBoundingClientRect().height).toBeGreaterThan(host!.getBoundingClientRect().height);
      return candidate!;
    });

    expect(host.contains(panel)).toBe(false);
    const rect = panel.getBoundingClientRect();
    const hit = document.elementFromPoint(rect.left + rect.width / 2, rect.top + rect.height / 2);
    expect(panel.contains(hit)).toBe(true);
  });

  it("moves focus into the panel, contains Tab navigation, and restores focus", async () => {
    host = document.createElement("div");
    document.body.append(host);

    app = mount(ActivityFilters, {
      target: host,
      props: {
        author: "",
        authorOptions: [{ name: "alice", label: "Alice" }],
        badgeCount: 1,
        sections: [],
        onAuthorSelect: vi.fn(),
        onReset: vi.fn(),
      },
    });

    const trigger = host.querySelector<HTMLButtonElement>("button[aria-label='Filters']");
    expect(trigger).not.toBeNull();
    trigger!.focus();
    trigger!.click();

    const panel = await vi.waitFor(() => {
      const candidate = document.querySelector<HTMLElement>("[aria-label='Activity filters']");
      expect(candidate).not.toBeNull();
      expect(candidate!.contains(document.activeElement)).toBe(true);
      return candidate!;
    });
    const controls = Array.from(panel.querySelectorAll<HTMLElement>("button:not(:disabled)"));
    expect(controls.length).toBeGreaterThan(1);
    expect(document.activeElement).toBe(controls[0]);

    controls.at(-1)!.focus();
    await userEvent.keyboard("{Tab}");
    expect(document.activeElement).toBe(controls[0]);

    controls[0]!.focus();
    await userEvent.keyboard("{Shift>}{Tab}{/Shift}");
    expect(document.activeElement).toBe(controls.at(-1));

    await userEvent.keyboard("{Escape}");
    await vi.waitFor(() => expect(document.querySelector("[aria-label='Activity filters']")).toBeNull());
    expect(document.activeElement).toBe(trigger);
  });

  it("uses roving focus and arrow-key selection within radio groups", async () => {
    host = document.createElement("div");
    document.body.append(host);
    const onFlat = vi.fn();
    const onThreaded = vi.fn();
    const onTimeline = vi.fn();

    app = mount(ActivityFilters, {
      target: host,
      props: {
        author: "",
        authorOptions: [],
        sections: [
          {
            title: "View",
            selectionMode: "single",
            items: [
              { id: "flat", label: "Flat", active: true, onSelect: onFlat },
              { id: "threaded", label: "Threaded", active: false, onSelect: onThreaded },
              { id: "timeline", label: "Timeline", active: false, onSelect: onTimeline },
            ],
          },
        ],
        onAuthorSelect: vi.fn(),
      },
    });

    host.querySelector<HTMLButtonElement>("button[aria-label='Filters']")!.click();
    const radios = await vi.waitFor(() => {
      const candidates = Array.from(document.querySelectorAll<HTMLButtonElement>("[role='radio']"));
      expect(candidates).toHaveLength(3);
      return candidates;
    });
    expect(radios.map((radio) => radio.tabIndex)).toEqual([0, -1, -1]);

    radios[0]!.focus();
    await userEvent.keyboard("{ArrowDown}");
    expect(onThreaded).toHaveBeenCalledOnce();
    expect(document.activeElement).toBe(radios[1]);

    await userEvent.keyboard("{End}");
    expect(onTimeline).toHaveBeenCalledOnce();
    expect(document.activeElement).toBe(radios[2]);

    await userEvent.keyboard("{Home}");
    expect(onFlat).toHaveBeenCalledOnce();
    expect(document.activeElement).toBe(radios[0]);

    await userEvent.keyboard("{Tab}");
    expect(document.activeElement).toBe(
      document.querySelector<HTMLButtonElement>("button[aria-label='Filter authors']"),
    );
  });
});
