import { afterEach, assert, it, vi } from "@effect/vitest";
import { Deferred, Effect, Fiber, Layer } from "effect";
import type { StoreInstances } from "../types.js";
import { DEFAULT_TERMINAL_SETTINGS, type TerminalSettings } from "../api/types.js";
import { TransientTransportError } from "../api/effect-errors.js";
import { StartupWorkflow, type StartupSnapshot } from "../app/startup-workflow.js";
import { appStartupProgram } from "./appStartup.js";

type LaunchTargets = NonNullable<StartupSnapshot["launch_targets"]>;

const codexTarget = {
  key: "codex",
  label: "Codex",
  kind: "agent",
  source: "builtin",
  command: ["codex"],
  available: true,
  disabled_reason: "",
} satisfies LaunchTargets[number];

function makeStores(
  options: {
    readonly syncPolling?: Effect.Effect<never>;
    readonly providerEvents?: Effect.Effect<never>;
  } = {},
): StoreInstances {
  let terminalSettings = { ...DEFAULT_TERMINAL_SETTINGS };
  let launchTargets: LaunchTargets = [];
  return {
    settings: {
      setConfiguredRepos: vi.fn(),
      setModeVisibility: vi.fn(),
      getTerminalSettings: () => terminalSettings,
      setTerminalSettings: vi.fn((settings: TerminalSettings) => {
        terminalSettings = settings;
      }),
      setPullRequestSettings: vi.fn(),
      setTerminalFontFamily: vi.fn(),
      getLaunchTargets: () => launchTargets,
      setLaunchTargets: vi.fn((targets: LaunchTargets) => {
        launchTargets = [...targets];
      }),
    },
    activity: {
      hydrateDefaults: vi.fn(),
      loadActivity: vi.fn(),
    },
    sync: {
      pollingEffect: options.syncPolling ?? Effect.never,
    },
    pulls: {
      loadPulls: vi.fn(),
    },
    issues: {
      hydrateDefaults: vi.fn(),
      loadIssues: vi.fn(),
    },
    events: {
      streamEffect: options.providerEvents ?? Effect.never,
    },
  } as unknown as StoreInstances;
}

const settings = {
  repos: [],
  pull_requests: { allow_mid_stack_merges: false, prefer_github_native_stacks: false },
  workspaces: { auto_assign_on_create: false, default_sidebar_view: "diff" },
  issues: { hide_bots: true },
  kata_projects: [],
  fleet: {
    enabled: false,
    sessions: {},
    peers: [],
    ssh_peers: [],
    restart_required: false,
  },
  activity: {
    view_mode: "threaded",
    time_range: "7d",
    hide_closed: false,
    hide_bots: false,
    collapse_threads: false,
    default_branch_retention_days: 90,
    default_branch_max_commits: 5000,
  },
  terminal: {
    font_family: '"Fira Code", monospace',
    font_size: 14,
    scrollback: 1000,
    line_height: 1,
    letter_spacing: 0,
    cursor_blink: true,
    font_ligatures: false,
    hide_tmux_status: false,
  },
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
  agents: [],
  launch_targets: [codexTarget],
} satisfies StartupSnapshot;

const SuccessfulStartup = Layer.succeed(StartupWorkflow)({
  start: Effect.succeed(settings),
  invalidate: Effect.void,
});

const FailedStartup = Layer.succeed(StartupWorkflow)({
  start: Effect.fail(TransientTransportError.make({ operation: "GET /settings", cause: new Error("offline") })),
  invalidate: Effect.void,
});

const PendingStartup = Layer.succeed(StartupWorkflow)({
  start: Effect.never,
  invalidate: Effect.void,
});

afterEach(() => {
  vi.restoreAllMocks();
});

it.layer(SuccessfulStartup)("application startup hydration", (it) => {
  it.effect("owns sync polling and provider events for the startup lifetime", () =>
    Effect.scoped(
      Effect.gen(function* () {
        let syncStarted = false;
        let syncReleased = false;
        let eventsStarted = false;
        let eventsReleased = false;
        const syncPolling = Effect.scoped(
          Effect.acquireRelease(
            Effect.sync(() => {
              syncStarted = true;
            }),
            () =>
              Effect.sync(() => {
                syncReleased = true;
              }),
          ).pipe(Effect.andThen(Effect.never)),
        );
        const providerEvents = Effect.scoped(
          Effect.acquireRelease(
            Effect.sync(() => {
              eventsStarted = true;
            }),
            () =>
              Effect.sync(() => {
                eventsReleased = true;
              }),
          ).pipe(Effect.andThen(Effect.never)),
        );
        const stores = makeStores({ syncPolling, providerEvents });
        const ready = yield* Deferred.make<void>();
        const fiber = yield* Effect.forkScoped(
          appStartupProgram({
            stores,
            onReady: () => Deferred.doneUnsafe(ready, Effect.void),
          }),
        );

        yield* Deferred.await(ready);
        yield* Effect.yieldNow;

        assert.strictEqual(syncStarted, true);
        assert.strictEqual(eventsStarted, true);

        yield* Fiber.interrupt(fiber);

        assert.strictEqual(syncReleased, true);
        assert.strictEqual(eventsReleased, true);
      }),
    ),
  );

  it.effect("hydrates settings before making the application ready", () =>
    Effect.scoped(
      Effect.gen(function* () {
        const stores = makeStores();
        const ready = yield* Deferred.make<void>();
        const pullsStarted = yield* Deferred.make<void>();
        const issuesStarted = yield* Deferred.make<void>();
        const beforeInitialLoad = vi.fn();
        vi.mocked(stores.pulls.loadPulls).mockImplementation(() => {
          Deferred.doneUnsafe(pullsStarted, Effect.void);
        });
        vi.mocked(stores.issues.loadIssues).mockImplementation(() => {
          Deferred.doneUnsafe(issuesStarted, Effect.void);
        });
        const program = appStartupProgram({
          stores,
          beforeInitialLoad,
          onReady: () => {
            Deferred.doneUnsafe(ready, Effect.void);
          },
        });

        const fiber = yield* Effect.forkScoped(program);
        yield* Deferred.await(ready);
        yield* Deferred.await(pullsStarted);
        yield* Deferred.await(issuesStarted);

        assert.deepStrictEqual(stores.settings.getLaunchTargets(), [codexTarget]);
        assert.strictEqual(vi.mocked(stores.settings.setConfiguredRepos).mock.calls.length, 1);
        assert.strictEqual(vi.mocked(stores.settings.setTerminalSettings).mock.calls.length, 1);
        assert.strictEqual(beforeInitialLoad.mock.calls.length, 1);
        assert.strictEqual(vi.mocked(stores.pulls.loadPulls).mock.calls.length, 1);
        assert.strictEqual(vi.mocked(stores.issues.loadIssues).mock.calls.length, 1);
        yield* Fiber.interrupt(fiber);
      }),
    ),
  );
});

it.layer(FailedStartup)("application startup defaults", (it) => {
  it.effect("continues with defaults after a settings failure", () =>
    Effect.scoped(
      Effect.gen(function* () {
        const stores = makeStores();
        const ready = yield* Deferred.make<void>();
        const warn = vi.spyOn(console, "warn").mockImplementation(() => undefined);
        const fiber = yield* Effect.forkScoped(
          appStartupProgram({
            stores,
            onReady: () => {
              Deferred.doneUnsafe(ready, Effect.void);
            },
          }),
        );

        yield* Deferred.await(ready);

        assert.strictEqual(vi.mocked(stores.settings.setConfiguredRepos).mock.calls.length, 0);
        assert.strictEqual(warn.mock.calls.length, 1);
        yield* Fiber.interrupt(fiber);
      }),
    ),
  );
});

it.layer(PendingStartup)("application startup interruption", (it) => {
  it.effect("stops silently before post-startup side effects", () =>
    Effect.scoped(
      Effect.gen(function* () {
        const stores = makeStores();
        const warn = vi.spyOn(console, "warn").mockImplementation(() => undefined);
        const onReady = vi.fn();
        const fiber = yield* Effect.forkScoped(appStartupProgram({ stores, onReady }));
        yield* Effect.yieldNow;

        yield* Fiber.interrupt(fiber);

        assert.strictEqual(onReady.mock.calls.length, 0);
        assert.strictEqual(warn.mock.calls.length, 0);
      }),
    ),
  );
});
