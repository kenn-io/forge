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

it.layer(StartupTestLayer)("shares concurrent startup settings demand", (it) => {
  it.effect("performs one settings request", () =>
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

      assert.deepStrictEqual(snapshots, [settings, settings]);
      assert.deepStrictEqual(observedPaths, ["/healthz", "/api/v1/settings"]);
    }),
  );
});

it.layer(StartupTestLayer)("startup recovery", (it) => {
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

  it.effect("times out while backend readiness never succeeds", () =>
    Effect.gen(function* () {
      vi.stubGlobal("fetch", (() => Promise.resolve(Response.json({}, { status: 503 }))) satisfies typeof fetch);

      const startup = yield* StartupWorkflow;
      const fiber = yield* Effect.forkChild(Effect.exit(startup.start));
      yield* TestClock.adjust("8 seconds");
      const exit = yield* Fiber.join(fiber);

      assert.isTrue(Exit.isFailure(exit));
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
