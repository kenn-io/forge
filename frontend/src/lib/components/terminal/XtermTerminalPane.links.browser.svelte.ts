import { mount, unmount } from "svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";
import { Effect } from "effect";
import { makeAppRuntime } from "../../app/runtime.js";
import { createSettingsStore } from "../../stores/settings.svelte.js";
import { STORES_KEY } from "../../context.js";
import XtermTerminalPaneTestHarness from "./XtermTerminalPaneTestHarness.svelte";
import "../../../app.css";

describe("terminal links", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it.each([
    { platform: "MacIntel", modifier: "Meta" as const, osc8: false },
    { platform: "MacIntel", modifier: "Meta" as const, osc8: true },
    { platform: "Linux x86_64", modifier: "Control" as const, osc8: false },
    { platform: "Linux x86_64", modifier: "Control" as const, osc8: true },
  ])("opens URLs on $platform with $modifier (OSC 8: $osc8)", async ({ platform, modifier, osc8 }) => {
    vi.spyOn(navigator, "platform", "get").mockReturnValue(platform);
    const open = vi.spyOn(window, "open").mockImplementation(() => null);
    let socket: WebSocket | undefined;
    vi.stubGlobal(
      "WebSocket",
      class extends EventTarget {
        static readonly OPEN = 1;
        readonly OPEN = 1;
        readyState = 0;
        binaryType = "arraybuffer";
        constructor() {
          super();
          socket = this as unknown as WebSocket;
          queueMicrotask(() => {
            this.readyState = 1;
            this.dispatchEvent(new Event("open"));
          });
        }
        send() {}
        close() {
          this.readyState = 3;
        }
      },
    );
    const runtime = makeAppRuntime();
    const settings = createSettingsStore();
    settings.setConfiguredRepos([
      {
        provider: "github",
        platform_host: "github.com",
        owner: "acme",
        name: "widgets",
        repo_path: "acme/widgets",
        hidden_from_ui: false,
        is_glob: false,
        issue_pr_references: true,
        matched_repo_count: 1,
      },
    ]);
    const target = document.createElement("div");
    target.style.cssText = "width: 800px; height: 400px";
    document.body.appendChild(target);
    const component = mount(XtermTerminalPaneTestHarness, {
      target,
      props: { runtime, websocketPath: "/ws/v1/workspaces/ws-1/runtime/sessions/s1/terminal", active: true },
      context: new Map([[STORES_KEY, { settings }]]),
    });
    try {
      await vi.waitFor(() => expect(socket?.readyState).toBe(1));
      socket!.dispatchEvent(new MessageEvent("message", { data: JSON.stringify({ type: "replay_ready" }) }));
      const screen = target.querySelector<HTMLElement>(".xterm-screen")!;
      const terminal = page.elementLocator(screen);

      for (const url of ["https://example.com/docs", "https://github.com/acme/widgets/pull/42"]) {
        // Exercise real xterm detection and pointer handling, including applications
        // that enable terminal mouse tracking. Only the transport and opener are stubbed.
        await page.elementLocator(target).hover({ position: { x: 700, y: 300 } });
        const text = osc8 ? `\x1b]8;;${url}\x07Open link\x1b]8;;\x07` : url;
        socket!.dispatchEvent(
          new MessageEvent("message", {
            data: new TextEncoder().encode(`\x1b[?1000h\x1b[2J\x1b[H${text}`).buffer,
          }),
        );
        await terminal.hover({ position: { x: 25, y: 8 } });
        await vi.waitFor(() => expect(target.querySelector(".terminal-link-tooltip span")?.textContent).toBe(url));

        await terminal.click({ position: { x: 25, y: 8 }, modifiers: [modifier] });
        expect(open).toHaveBeenCalledExactlyOnceWith(url, "_blank", "noopener,noreferrer");
        open.mockClear();
      }
    } finally {
      await unmount(component);
      target.remove();
      await Effect.runPromise(runtime.disposeEffect);
    }
  });
});
