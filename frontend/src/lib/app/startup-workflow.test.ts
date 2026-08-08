import { afterEach, assert, it, vi } from "@effect/vitest";
import { Effect, Exit, Fiber, Layer } from "effect";
import { TestClock } from "effect/testing";
import { GeneratedApiLive } from "../api/generated-api.js";
import type { components } from "../api/generated/schema.js";
import { StreamingFetchLive } from "../browser/streaming-fetch.js";
import { StartupWorkflow, StartupWorkflowLive, waitUntilBackendReady } from "./startup-workflow.js";

type SettingsResponse = components["schemas"]["SettingsResponse"];

const StartupTestLayer = Layer.provideMerge(StartupWorkflowLive, Layer.mergeAll(GeneratedApiLive, StreamingFetchLive));

afterEach(() => {
  vi.unstubAllGlobals();
});

it.layer(StartupTestLayer)("shares startup settings demand", (it) => {
  it.effect("performs one settings request until invalidated", () =>
    Effect.gen(function* () {
      const settings = {
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
        terminal: {
          font_family: "",
          font_size: 12,
          scrollback: 1000,
          line_height: 1,
          letter_spacing: 0,
          cursor_blink: true,
          font_ligatures: false,
          hide_tmux_status: false,
        },
        workspaces: { auto_assign_on_create: false },
      } satisfies SettingsResponse;
      const observedPaths: string[] = [];
      const fetch: typeof globalThis.fetch = (input, init) => {
        const request = input instanceof Request ? input : new Request(input, init);
        observedPaths.push(new URL(request.url).pathname);
        if (new URL(request.url).pathname.endsWith("/healthz")) {
          return Promise.resolve(Response.json({ ok: true }));
        }
        return Promise.resolve(Response.json(settings));
      };
      vi.stubGlobal("fetch", fetch);

      const startup = yield* StartupWorkflow;
      const snapshots = yield* Effect.all([startup.start, startup.start], { concurrency: "unbounded" });
      const laterSnapshot = yield* startup.start;

      assert.deepStrictEqual(snapshots, [settings, settings]);
      assert.deepStrictEqual(laterSnapshot, settings);
      assert.deepStrictEqual(observedPaths, ["/healthz", "/api/v1/settings"]);
    }),
  );
});

it.layer(StartupTestLayer)("startup invalidation", (it) => {
  it.effect("does not publish a settings snapshot invalidated while its request is in flight", () =>
    Effect.gen(function* () {
      const first = {
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
        pull_requests: { allow_mid_stack_merges: false, prefer_github_native_stacks: false },
        repos: [],
        terminal: {
          font_family: "",
          font_size: 12,
          scrollback: 1000,
          line_height: 1,
          letter_spacing: 0,
          cursor_blink: true,
          font_ligatures: false,
          hide_tmux_status: false,
        },
        workspaces: { auto_assign_on_create: false },
      } satisfies SettingsResponse;
      const second = { ...first, activity: { ...first.activity, hide_bots: true } };
      let releaseFirst = () => {};
      const firstGate = new Promise<void>((resolve) => {
        releaseFirst = resolve;
      });
      let settingsRequests = 0;
      const fetch: typeof globalThis.fetch = (input, init) => {
        const request = input instanceof Request ? input : new Request(input, init);
        if (new URL(request.url).pathname.endsWith("/healthz")) {
          return Promise.resolve(Response.json({ ok: true }));
        }
        settingsRequests += 1;
        return settingsRequests === 1
          ? firstGate.then(() => Response.json(first))
          : Promise.resolve(Response.json(second));
      };
      vi.stubGlobal("fetch", fetch);

      const startup = yield* StartupWorkflow;
      const pending = yield* Effect.forkChild(startup.start);
      yield* Effect.yieldNow;
      yield* startup.invalidate;
      releaseFirst();
      const result = yield* Fiber.join(pending);

      assert.isTrue(result.activity.hide_bots);
      assert.strictEqual(settingsRequests, 2);
    }),
  );
});

it.layer(StartupTestLayer)("startup retry after failure", (it) => {
  it.effect("requests settings again after a failed startup lookup", () =>
    Effect.gen(function* () {
      let settingsRequests = 0;
      const settings = {
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
        terminal: {
          font_family: "",
          font_size: 12,
          scrollback: 1000,
          line_height: 1,
          letter_spacing: 0,
          cursor_blink: true,
          font_ligatures: false,
          hide_tmux_status: false,
        },
        workspaces: { auto_assign_on_create: false },
      } satisfies SettingsResponse;
      const fetch: typeof globalThis.fetch = (input, init) => {
        const request = input instanceof Request ? input : new Request(input, init);
        if (new URL(request.url).pathname.endsWith("/healthz")) {
          return Promise.resolve(Response.json({ ok: true }));
        }
        settingsRequests += 1;
        return settingsRequests === 1
          ? Promise.reject(new TypeError("settings transport failed"))
          : Promise.resolve(Response.json(settings));
      };
      vi.stubGlobal("fetch", fetch);

      const startup = yield* StartupWorkflow;
      const first = yield* Effect.exit(startup.start);
      const second = yield* startup.start;

      assert.isTrue(Exit.isFailure(first));
      assert.deepStrictEqual(second, settings);
      assert.strictEqual(settingsRequests, 2);
    }),
  );
});

it.layer(StartupTestLayer)("startup timeout", (it) => {
  it.effect("keeps waiting for backend readiness beyond the settings request timeout", () =>
    Effect.gen(function* () {
      let ready = false;
      const settingsResponse = { delayed: true };
      vi.stubGlobal("fetch", ((input: RequestInfo | URL) => {
        const request = input instanceof Request ? input : new Request(input);
        const pathname = new URL(request.url).pathname;
        if (pathname.endsWith("/healthz")) {
          return Promise.resolve(Response.json({}, { status: ready ? 200 : 503 }));
        }
        return Promise.resolve(Response.json(settingsResponse));
      }) satisfies typeof fetch);

      const startup = yield* StartupWorkflow;
      const fiber = yield* Effect.forkChild(startup.start);
      yield* Effect.yieldNow;
      yield* TestClock.adjust("8 seconds");
      assert.isUndefined(fiber.pollUnsafe());

      ready = true;
      yield* TestClock.adjust("1500 millis");
      yield* Effect.yieldNow;
      assert.deepStrictEqual(yield* Fiber.join(fiber), settingsResponse);
    }),
  );
});

it.layer(StreamingFetchLive)("backend readiness polling", (it) => {
  it.effect("retries transient failures without overlapping requests", () =>
    Effect.gen(function* () {
      let activeRequests = 0;
      let maximumActiveRequests = 0;
      let attempt = 0;
      const fetch: typeof globalThis.fetch = () => {
        activeRequests += 1;
        maximumActiveRequests = Math.max(maximumActiveRequests, activeRequests);
        attempt += 1;
        const currentAttempt = attempt;
        const response =
          currentAttempt === 1
            ? Promise.reject(new TypeError("backend is restarting"))
            : Promise.resolve(Response.json({}, { status: currentAttempt === 2 ? 503 : 200 }));
        return response.finally(() => {
          activeRequests -= 1;
        });
      };
      vi.stubGlobal("fetch", fetch);

      const readiness = yield* Effect.forkChild(waitUntilBackendReady);
      yield* Effect.yieldNow;
      yield* TestClock.adjust("750 millis");
      yield* Effect.yieldNow;
      yield* TestClock.adjust("750 millis");
      yield* Fiber.join(readiness);

      assert.strictEqual(attempt, 3);
      assert.strictEqual(maximumActiveRequests, 1);
    }),
  );
});
