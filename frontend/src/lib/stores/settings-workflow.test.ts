import { afterEach, assert, it, vi } from "@effect/vitest";
import { Effect, Fiber, Layer } from "effect";
import { GeneratedApiLive } from "../api/generated-api.js";
import type { components } from "../api/generated/schema.js";
import { StreamingFetchLive } from "../browser/streaming-fetch.js";
import { StartupWorkflowLive } from "../app/startup-workflow.js";
import { SettingsWorkflow, SettingsWorkflowLive } from "./settings-workflow.js";

type SettingsResponse = components["schemas"]["SettingsResponse"];

const StartupTestLayer = Layer.provideMerge(StartupWorkflowLive, Layer.mergeAll(GeneratedApiLive, StreamingFetchLive));
const SettingsTestLayer = Layer.provideMerge(SettingsWorkflowLive, StartupTestLayer);

afterEach(() => {
  vi.unstubAllGlobals();
});

it.layer(SettingsTestLayer)("ordered settings writes", (it) => {
  it.effect("persists commands in submission order", () =>
    Effect.gen(function* () {
      let releaseFirstResponse = () => {};
      const firstResponse = new Promise<void>((resolve) => {
        releaseFirstResponse = resolve;
      });
      const observedFontSizes: number[] = [];
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
        return request
          .clone()
          .json()
          .then((body: unknown) => {
            if (
              typeof body !== "object" ||
              body === null ||
              !("terminal" in body) ||
              typeof body.terminal !== "object" ||
              body.terminal === null ||
              !("font_size" in body.terminal) ||
              typeof body.terminal.font_size !== "number"
            ) {
              return Response.json({ detail: "invalid settings request" }, { status: 400 });
            }
            observedFontSizes.push(body.terminal.font_size);
            const response = Response.json({
              ...settings,
              terminal: { ...settings.terminal, font_size: body.terminal.font_size },
            });
            if (observedFontSizes.length === 1) {
              return firstResponse.then(() => response);
            }
            return response;
          });
      };
      vi.stubGlobal("fetch", fetch);

      const workflow = yield* SettingsWorkflow;
      const first = yield* Effect.forkChild(
        workflow.enqueue({ request: Effect.succeed({ terminal: { ...settings.terminal, font_size: 13 } }) }),
      );
      yield* Effect.yieldNow;
      const second = yield* Effect.forkChild(
        workflow.enqueue({ request: Effect.succeed({ terminal: { ...settings.terminal, font_size: 14 } }) }),
      );
      yield* Effect.yieldNow;

      assert.deepStrictEqual(observedFontSizes, [13]);
      releaseFirstResponse();
      yield* Fiber.join(first);
      yield* Fiber.join(second);
      assert.deepStrictEqual(observedFontSizes, [13, 14]);
    }),
  );
});
