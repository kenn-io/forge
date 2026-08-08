import { DEFAULT_TERMINAL_SETTINGS, type TerminalSettings } from "../../api/types.js";
import { Effect } from "effect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { makeAppRuntime, type OwnedAppRuntime } from "../../app/runtime.js";
import type { StartupSnapshot } from "../../app/startup-workflow.js";
import {
  MAX_TERMINAL_FONT_SIZE,
  MIN_TERMINAL_FONT_SIZE,
  RESET_TERMINAL_FONT_SIZE,
  createTerminalZoomController,
} from "./terminalZoom";

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, reject, resolve };
}

function createHarness(
  persist: (settings: TerminalSettings) => Promise<TerminalSettings> = async (settings) => settings,
) {
  persistSettings = persist;
  const runtime = makeAppRuntime();
  runtimes.push(runtime);
  let settings = { ...DEFAULT_TERMINAL_SETTINGS };
  const reportError = vi.fn();
  const store = {
    getTerminalSettings: () => settings,
    setTerminalSettings: (next: TerminalSettings) => {
      settings = next;
    },
  };
  const controller = createTerminalZoomController({
    runtime,
    reportError,
    store,
  });
  return {
    controller,
    getSettings: () => settings,
    reportError,
  };
}

let persistSettings: (settings: TerminalSettings) => Promise<TerminalSettings>;
let persistedTerminal: TerminalSettings;
const runtimes: OwnedAppRuntime[] = [];

function settingsResponse(terminal: TerminalSettings): StartupSnapshot {
  return {
    activity: {
      view_mode: "threaded",
      time_range: "7d",
      hide_closed: false,
      hide_bots: false,
      collapse_threads: false,
      default_branch_retention_days: 90,
      default_branch_max_commits: 5000,
    },
    agents: [],
    fleet: {
      enabled: false,
      sessions: {},
      peers: [],
      ssh_peers: [],
      restart_required: false,
    },
    issues: { hide_bots: true },
    kata_projects: [],
    launch_targets: [],
    modes: {
      activity: true,
      repos: true,
      kata: false,
      docs: false,
      pulls: true,
      issues: true,
      reviews: true,
      workspaces: true,
    },
    notifications: { enabled: true },
    pull_requests: {
      allow_mid_stack_merges: false,
      prefer_github_native_stacks: false,
    },
    repos: [],
    terminal,
    workspaces: { auto_assign_on_create: false },
  };
}

beforeEach(() => {
  persistSettings = (settings) => Promise.resolve(settings);
  persistedTerminal = { ...DEFAULT_TERMINAL_SETTINGS };
  const fetch: typeof globalThis.fetch = async (input, init) => {
    const request = input instanceof Request ? input : new Request(input, init);
    if (request.method === "GET") {
      return Response.json(settingsResponse(persistedTerminal));
    }
    const body = await request.clone().json();
    if (
      typeof body !== "object" ||
      body === null ||
      !("terminal" in body) ||
      typeof body.terminal !== "object" ||
      body.terminal === null
    ) {
      return Response.json({ detail: "invalid terminal settings" }, { status: 400 });
    }
    const terminal = body.terminal as TerminalSettings;
    persistedTerminal = await persistSettings(terminal);
    return Response.json(settingsResponse(persistedTerminal));
  };
  vi.stubGlobal("fetch", fetch);
});

afterEach(async () => {
  for (const runtime of runtimes.splice(0)) {
    await Effect.runPromise(runtime.disposeEffect);
  }
  vi.unstubAllGlobals();
});

describe("terminal zoom controller", () => {
  it("uses the shared 12px terminal default as its reset target", () => {
    expect(DEFAULT_TERMINAL_SETTINGS.font_size).toBe(12);
    expect(RESET_TERMINAL_FONT_SIZE).toBe(DEFAULT_TERMINAL_SETTINGS.font_size);
  });

  it("updates the shared settings immediately and clamps persisted font sizes", async () => {
    const persist = vi.fn(async (settings: TerminalSettings) => settings);
    const harness = createHarness(persist);

    harness.controller.setFontSize(MAX_TERMINAL_FONT_SIZE + 10);
    expect(harness.getSettings().font_size).toBe(MAX_TERMINAL_FONT_SIZE);
    await Effect.runPromise(harness.controller.whenIdle());
    expect(persist).toHaveBeenLastCalledWith(expect.objectContaining({ font_size: MAX_TERMINAL_FONT_SIZE }));

    harness.controller.setFontSize(MIN_TERMINAL_FONT_SIZE - 10);
    expect(harness.getSettings().font_size).toBe(MIN_TERMINAL_FONT_SIZE);
    await Effect.runPromise(harness.controller.whenIdle());
    expect(persist).toHaveBeenLastCalledWith(expect.objectContaining({ font_size: MIN_TERMINAL_FONT_SIZE }));
  });

  it("serializes rapid saves without letting an older response overwrite the latest zoom", async () => {
    const first = deferred<TerminalSettings>();
    const second = deferred<TerminalSettings>();
    const persist = vi
      .fn<(settings: TerminalSettings) => Promise<TerminalSettings>>()
      .mockImplementationOnce(() => first.promise)
      .mockImplementationOnce(() => second.promise);
    const harness = createHarness(persist);

    harness.controller.increase();
    harness.controller.increase();
    expect(harness.getSettings().font_size).toBe(14);
    await vi.waitFor(() => expect(persist).toHaveBeenCalledTimes(1));

    first.resolve({ ...DEFAULT_TERMINAL_SETTINGS, font_size: 13 });
    await vi.waitFor(() => expect(persist).toHaveBeenCalledTimes(2));
    expect(harness.getSettings().font_size).toBe(14);

    second.resolve({ ...DEFAULT_TERMINAL_SETTINGS, font_size: 14 });
    await Effect.runPromise(harness.controller.whenIdle());
    expect(harness.getSettings().font_size).toBe(14);
  });

  it("rolls back the latest failed save to the last confirmed font size", async () => {
    const persist = vi.fn(async () => {
      throw new Error("settings unavailable");
    });
    const harness = createHarness(persist);

    harness.controller.increase();
    expect(harness.getSettings().font_size).toBe(13);
    await Effect.runPromise(harness.controller.whenIdle());

    expect(harness.getSettings().font_size).toBe(RESET_TERMINAL_FONT_SIZE);
    expect(harness.reportError).toHaveBeenCalledWith(expect.objectContaining({ _tag: "TransientTransportError" }));
  });

  it("keeps an accepted save alive when its controller is disposed", async () => {
    const accepted = deferred<TerminalSettings>();
    const persist = vi.fn(() => accepted.promise);
    const harness = createHarness(persist);

    harness.controller.increase();
    await vi.waitFor(() => expect(persist).toHaveBeenCalledOnce());
    harness.controller.dispose();
    accepted.resolve({ ...DEFAULT_TERMINAL_SETTINGS, font_size: 13 });
    await Effect.runPromise(harness.controller.whenIdle());

    expect(harness.getSettings().font_size).toBe(13);
    expect(harness.reportError).not.toHaveBeenCalled();
  });
});
