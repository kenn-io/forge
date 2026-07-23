import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";
import { DEFAULT_TERMINAL_SETTINGS, type TerminalSettings } from "@middleman/ui/api/types";

import { createTerminalZoomController } from "./lib/components/terminal/terminalZoom.js";
import {
  emitBrowserEventSource,
  firePopstate,
  getBrowserEventSourceCount,
  mountBrowserApp,
  type MountedBrowserApp,
} from "./test/browserAppHarness.js";
import { mockSettings } from "./test/mockApiFetch.js";

const WAIT = 10_000;
const settingsStoreCapture = vi.hoisted(() => ({
  current: undefined as
    | {
        getTerminalSettings(): TerminalSettings;
        setTerminalSettings(settings: TerminalSettings): void;
      }
    | undefined,
}));

vi.mock("@middleman/ui/stores/settings", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@middleman/ui/stores/settings")>();
  return {
    ...actual,
    createSettingsStore: () => {
      const store = actual.createSettingsStore();
      settingsStoreCapture.current = store;
      return store;
    },
  };
});

function navLabels(): string[] {
  return Array.from(document.querySelectorAll<HTMLElement>(".kit-settings__nav-label")).map(
    (element) => element.textContent?.trim() ?? "",
  );
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((resolvePromise) => {
    resolve = resolvePromise;
  });
  return { promise, resolve };
}

describe("terminal settings response races", () => {
  let mounted: MountedBrowserApp | null = null;

  afterEach(() => {
    mounted?.unmount();
    mounted = null;
    settingsStoreCapture.current = undefined;
  });

  it("preserves a newer terminal zoom when a settings save resolves after navigation", async () => {
    await page.viewport(1280, 900);
    const savedFontFamily = '"Saved Font", monospace';
    let releaseSaveResponse: (() => void) | undefined;
    mounted = await mountBrowserApp("/settings", {
      overrides: [
        (request) => {
          if (request.method !== "PUT" || request.url.pathname !== "/api/v1/settings") return null;
          const responseBody = new ReadableStream<Uint8Array>({
            start(controller) {
              releaseSaveResponse = () => {
                controller.enqueue(
                  new TextEncoder().encode(
                    JSON.stringify({
                      terminal: {
                        ...mockSettings.terminal,
                        font_family: savedFontFamily,
                      },
                    }),
                  ),
                );
                controller.close();
              };
            },
          });
          return new Response(responseBody, {
            headers: { "content-type": "application/json" },
          });
        },
      ],
    });
    await vi.waitFor(() => expect(navLabels()).toHaveLength(8), WAIT);

    const terminalButton = Array.from(document.querySelectorAll<HTMLButtonElement>(".kit-settings__nav-item")).find(
      (button) => button.textContent?.includes("Terminal"),
    );
    terminalButton!.click();
    const terminalPanel = await vi.waitFor(() => {
      const panel = document.querySelector<HTMLElement>(".settings-panel:not([hidden])");
      expect(panel?.textContent).toContain("Terminal");
      return panel!;
    }, WAIT);
    const fontInput = terminalPanel.querySelector<HTMLInputElement>("#terminal-font-family")!;
    fontInput.value = '"Draft Font", monospace';
    fontInput.dispatchEvent(new Event("input", { bubbles: true }));
    const saveButton = Array.from(terminalPanel.querySelectorAll<HTMLButtonElement>("button")).find(
      (button) => button.textContent?.trim() === "Save",
    )!;
    await vi.waitFor(() => expect(saveButton.disabled).toBe(false), WAIT);
    saveButton.click();
    await vi.waitFor(
      () =>
        expect(
          mounted!.api.requests.filter(
            (request) => request.method === "PUT" && request.url.pathname === "/api/v1/settings",
          ),
        ).toHaveLength(1),
      WAIT,
    );

    document.querySelector<HTMLButtonElement>(".settings-page .back-button")!.click();
    await vi.waitFor(() => expect(document.querySelector(".settings-page")).toBeNull(), WAIT);

    const settingsStore = settingsStoreCapture.current!;
    settingsStore.setTerminalSettings({
      ...settingsStore.getTerminalSettings(),
      font_size: 13,
    });
    releaseSaveResponse!();

    await vi.waitFor(() => expect(settingsStore.getTerminalSettings().font_family).toBe(savedFontFamily), WAIT);
    expect(settingsStore.getTerminalSettings().font_size).toBe(13);
  });

  it("preserves a pending zoom when a stale settings hydration resolves", async () => {
    await page.viewport(1280, 900);
    let delaySettingsHydration = false;
    let releaseSettingsHydration: (() => void) | undefined;
    mounted = await mountBrowserApp("/", {
      overrides: [
        (request) => {
          if (!delaySettingsHydration || request.method !== "GET" || request.url.pathname !== "/api/v1/settings") {
            return null;
          }
          delaySettingsHydration = false;
          const responseBody = new ReadableStream<Uint8Array>({
            start(controller) {
              releaseSettingsHydration = () => {
                controller.enqueue(
                  new TextEncoder().encode(
                    JSON.stringify({
                      ...mockSettings,
                      terminal: {
                        ...mockSettings.terminal,
                        font_family: '"Hydrated Font", monospace',
                      },
                    }),
                  ),
                );
                controller.close();
              };
            },
          });
          return new Response(responseBody, {
            headers: { "content-type": "application/json" },
          });
        },
      ],
    });
    await vi.waitFor(() => expect(document.querySelector(".activity-feed")).not.toBeNull(), WAIT);

    delaySettingsHydration = true;
    firePopstate("/settings");
    await vi.waitFor(() => expect(releaseSettingsHydration).toBeTypeOf("function"), WAIT);
    firePopstate("/");
    await vi.waitFor(() => expect(document.querySelector(".settings-page")).toBeNull(), WAIT);

    const pendingSave = deferred<TerminalSettings>();
    const settingsStore = settingsStoreCapture.current!;
    const zoom = createTerminalZoomController({
      store: settingsStore,
      persist: () => pendingSave.promise,
      reportError: vi.fn(),
    });
    zoom.setFontSize(13);
    expect(settingsStore.getTerminalSettings().font_size).toBe(13);

    releaseSettingsHydration!();
    await vi.waitFor(
      () => expect(settingsStore.getTerminalSettings().font_family).toBe('"Hydrated Font", monospace'),
      WAIT,
    );
    expect(settingsStore.getTerminalSettings().font_size).toBe(13);

    pendingSave.resolve({
      ...DEFAULT_TERMINAL_SETTINGS,
      font_size: 13,
    });
    await zoom.whenIdle();
    expect(settingsStore.getTerminalSettings().font_size).toBe(13);
  });

  it("preserves an in-flight zoom when a config reload resolves", async () => {
    await page.viewport(1280, 900);
    let delayConfigReload = false;
    let releaseConfigReload: (() => void) | undefined;
    mounted = await mountBrowserApp("/", {
      overrides: [
        (request) => {
          if (!delayConfigReload || request.method !== "GET" || request.url.pathname !== "/api/v1/settings") {
            return null;
          }
          delayConfigReload = false;
          const responseBody = new ReadableStream<Uint8Array>({
            start(controller) {
              releaseConfigReload = () => {
                controller.enqueue(
                  new TextEncoder().encode(
                    JSON.stringify({
                      ...mockSettings,
                      terminal: {
                        ...mockSettings.terminal,
                        font_family: '"Reloaded Font", monospace',
                      },
                    }),
                  ),
                );
                controller.close();
              };
            },
          });
          return new Response(responseBody, {
            headers: { "content-type": "application/json" },
          });
        },
      ],
    });
    await vi.waitFor(() => expect(document.querySelector(".activity-feed")).not.toBeNull(), WAIT);
    await vi.waitFor(() => expect(getBrowserEventSourceCount()).toBe(1), WAIT);

    const pendingSave = deferred<TerminalSettings>();
    const settingsStore = settingsStoreCapture.current!;
    const zoom = createTerminalZoomController({
      store: settingsStore,
      persist: () => pendingSave.promise,
      reportError: vi.fn(),
    });
    zoom.setFontSize(13);

    delayConfigReload = true;
    emitBrowserEventSource("config.changed", {
      valid: true,
      restart_required: false,
    });
    await vi.waitFor(() => expect(releaseConfigReload).toBeTypeOf("function"), WAIT);
    releaseConfigReload!();
    await vi.waitFor(
      () => expect(settingsStore.getTerminalSettings().font_family).toBe('"Reloaded Font", monospace'),
      WAIT,
    );
    const fontSizeAfterReload = settingsStore.getTerminalSettings().font_size;

    pendingSave.resolve({
      ...DEFAULT_TERMINAL_SETTINGS,
      font_size: 13,
    });
    await zoom.whenIdle();

    expect(fontSizeAfterReload).toBe(13);
    expect(settingsStore.getTerminalSettings().font_size).toBe(13);
  });
});
