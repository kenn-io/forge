import { assert, describe, it } from "@effect/vitest";
import { Deferred, Effect, Fiber, Option, Ref } from "effect";
import { TransientTransportError } from "../../api/effect-errors.js";
import { GeneratedApiLive } from "../../api/generated-api.js";
import type { WorkspaceRuntimeState } from "../../api/workspace-runtime.js";
import type { RuntimeSession } from "../../api/types.js";
import type { WorkspaceItemIdentity } from "../../workspace-inline.js";
import { defaultTerminalLayout } from "./terminal-layout.js";
import type { WorkspaceDetail } from "./workspace-detail.js";
import type { WorkflowPreset } from "./workflow-presets.js";
import {
  makeWorkspaceRuntimeWorkflow,
  WorkspaceRuntimePortLive,
  type WorkspaceRuntimeDeleteResult,
  type WorkspaceRuntimeMutationState,
  type WorkspaceRuntimePort,
} from "./workspace-runtime-workflow.js";

const emptyRuntime: WorkspaceRuntimeState = {
  launch_targets: [],
  sessions: [],
};

const refreshedWorkspace: WorkspaceDetail = {
  created_at: "2026-08-05T00:00:00Z",
  enrichment_status: "fresh",
  git_head_ref: "feature/effect-runtime",
  id: "ws-1",
  item_number: 7,
  item_type: "pull_request",
  platform_host: "github.com",
  repo_name: "widget",
  repo_owner: "acme",
  status: "ready",
  tmux_session: "kenn-forge-ws-1",
  worktree_path: "/tmp/worktree",
  repo: {
    name: "widget",
    owner: "acme",
    platform_host: "github.com",
    provider: "github",
    repo_path: "acme/widget",
  },
};

const workspaceIdentity: WorkspaceItemIdentity = {
  provider: "github",
  platformHost: "github.com",
  owner: "acme",
  name: "widget",
  repoPath: "acme/widget",
  number: 7,
  itemType: "pull_request",
};

function unusedPortMethod(): Effect.Effect<never> {
  return Effect.never;
}

describe("WorkspaceRuntimeWorkflow", () => {
  it.effect("aborts the generated runtime request when its read owner is interrupted", () =>
    Effect.scoped(
      Effect.gen(function* () {
        const originalFetch = globalThis.fetch;
        const requestStarted = Promise.withResolvers<AbortSignal>();
        const fetchImpl: typeof fetch = (input, init) => {
          const request = new Request(input, init);
          requestStarted.resolve(request.signal);
          return new Promise((_resolve, reject) => {
            request.signal.addEventListener("abort", () => reject(request.signal.reason), { once: true });
          });
        };
        yield* Effect.acquireRelease(
          Effect.sync(() => {
            globalThis.fetch = fetchImpl;
          }),
          () =>
            Effect.sync(() => {
              globalThis.fetch = originalFetch;
            }),
        );
        const port = yield* WorkspaceRuntimePortLive.pipe(Effect.provide(GeneratedApiLive));
        const read = yield* Effect.forkChild(port.read({ workspaceId: "ws-1" }));
        const signal = yield* Effect.promise(() => requestStarted.promise);

        yield* Fiber.interrupt(read);

        assert.isTrue(signal.aborted);
      }),
    ),
  );

  it.effect("shares an in-flight runtime read for one owner", () =>
    Effect.scoped(
      Effect.gen(function* () {
        const calls = yield* Ref.make(0);
        const result = yield* Deferred.make<WorkspaceRuntimeState>();
        const port: WorkspaceRuntimePort = {
          read: () => Ref.update(calls, (count) => count + 1).pipe(Effect.andThen(Deferred.await(result))),
          launch: unusedPortMethod,
          rename: unusedPortMethod,
          stop: unusedPortMethod,
          refresh: unusedPortMethod,
          retry: unusedPortMethod,
          delete: unusedPortMethod,
        };
        const workflow = yield* makeWorkspaceRuntimeWorkflow(port);

        const first = yield* Effect.forkChild(workflow.read("surface-a", "ws-1"));
        yield* Effect.yieldNow;
        const second = yield* Effect.forkChild(workflow.read("surface-a", "ws-1"));
        yield* Effect.yieldNow;

        assert.strictEqual(yield* Ref.get(calls), 1);
        yield* Deferred.succeed(result, emptyRuntime);
        assert.deepStrictEqual(yield* Fiber.join(first), Option.some(emptyRuntime));
        assert.deepStrictEqual(yield* Fiber.join(second), Option.some(emptyRuntime));
      }),
    ),
  );

  it.effect("replaces an older read when a forced refresh starts", () =>
    Effect.scoped(
      Effect.gen(function* () {
        const firstResult = yield* Deferred.make<WorkspaceRuntimeState>();
        const calls = yield* Ref.make(0);
        const freshRuntime: WorkspaceRuntimeState = {
          launch_targets: [],
          sessions: [
            {
              key: "ws-1:helper",
              workspace_id: "ws-1",
              target_key: "helper",
              label: "Helper",
              kind: "agent",
              status: "running",
              created_at: "2026-08-05T00:00:00Z",
              display_region: "workflow",
            },
          ],
        };
        const port: WorkspaceRuntimePort = {
          read: () =>
            Ref.getAndUpdate(calls, (count) => count + 1).pipe(
              Effect.flatMap((count) => (count === 0 ? Deferred.await(firstResult) : Effect.succeed(freshRuntime))),
            ),
          launch: unusedPortMethod,
          rename: unusedPortMethod,
          stop: unusedPortMethod,
          refresh: unusedPortMethod,
          retry: unusedPortMethod,
          delete: unusedPortMethod,
        };
        const workflow = yield* makeWorkspaceRuntimeWorkflow(port);

        const stale = yield* Effect.forkChild(workflow.read("surface-a", "ws-1"));
        yield* Effect.yieldNow;
        const fresh = yield* workflow.read("surface-a", "ws-1", undefined, { force: true });

        assert.deepStrictEqual(fresh, Option.some(freshRuntime));
        assert.deepStrictEqual(yield* Fiber.join(stale), Option.none());
      }),
    ),
  );

  it.effect("stops only the departing owner's read", () =>
    Effect.scoped(
      Effect.gen(function* () {
        const port: WorkspaceRuntimePort = {
          read: () => Effect.never,
          launch: unusedPortMethod,
          rename: unusedPortMethod,
          stop: unusedPortMethod,
          refresh: unusedPortMethod,
          retry: unusedPortMethod,
          delete: unusedPortMethod,
        };
        const workflow = yield* makeWorkspaceRuntimeWorkflow(port);
        const departing = yield* Effect.forkChild(workflow.read("surface-a", "ws-1"));
        const retained = yield* Effect.forkChild(workflow.read("surface-b", "ws-2"));
        yield* Effect.yieldNow;

        yield* workflow.release("surface-a");

        assert.deepStrictEqual(yield* Fiber.join(departing), Option.none());
        assert.isUndefined(retained.pollUnsafe());
        yield* Fiber.interrupt(retained);
      }),
    ),
  );

  it.effect("delivers a completed launch only to the replacement presenter", () =>
    Effect.scoped(
      Effect.gen(function* () {
        const launched = yield* Deferred.make<RuntimeSession>();
        const replacementReceived = yield* Deferred.make<void>();
        const firstStates: WorkspaceRuntimeMutationState[] = [];
        const replacementStates: WorkspaceRuntimeMutationState[] = [];
        const port: WorkspaceRuntimePort = {
          read: () => Effect.succeed(emptyRuntime),
          launch: () => Deferred.await(launched),
          rename: unusedPortMethod,
          stop: unusedPortMethod,
          refresh: unusedPortMethod,
          retry: unusedPortMethod,
          delete: unusedPortMethod,
        };
        const workflow = yield* makeWorkspaceRuntimeWorkflow(port);
        const target = { workspaceId: "ws-1" };
        yield* workflow.claimPresenter(target, "first", (state) =>
          Effect.sync(() => {
            firstStates.push(state);
            return false;
          }),
        );

        yield* workflow.launch(target, "helper", "workflow", { _tag: "Workflow" });
        yield* workflow.releasePresenter(target, "first");
        yield* workflow.claimPresenter(target, "replacement", (state) =>
          Effect.gen(function* () {
            replacementStates.push(state);
            if (state.kind !== "succeeded") return false;
            yield* Deferred.succeed(replacementReceived, undefined);
            return true;
          }),
        );
        yield* Deferred.succeed(launched, {
          key: "ws-1:helper",
          workspace_id: "ws-1",
          target_key: "helper",
          label: "Helper",
          kind: "agent",
          status: "running",
          created_at: "2026-08-05T00:00:00Z",
          display_region: "workflow",
        });
        yield* Deferred.await(replacementReceived);

        assert.deepStrictEqual(
          firstStates.map((state) => state.kind),
          ["pending"],
        );
        assert.deepStrictEqual(
          replacementStates.map((state) => state.kind),
          ["pending", "succeeded"],
        );
      }),
    ),
  );

  it.effect("interrupts a stale observer before a replacement presenter publishes success", () =>
    Effect.scoped(
      Effect.gen(function* () {
        const launched = yield* Deferred.make<RuntimeSession>();
        const oldDeliveryStarted = yield* Deferred.make<void>();
        const oldDeliveryInterrupted = yield* Deferred.make<void>();
        const replacementDelivered = yield* Deferred.make<void>();
        const port: WorkspaceRuntimePort = {
          read: () => Effect.succeed(emptyRuntime),
          launch: () => Deferred.await(launched),
          rename: unusedPortMethod,
          stop: unusedPortMethod,
          refresh: unusedPortMethod,
          retry: unusedPortMethod,
          delete: unusedPortMethod,
        };
        const workflow = yield* makeWorkspaceRuntimeWorkflow(port);
        const target = { workspaceId: "ws-1" };
        yield* workflow.claimPresenter(target, "old", (state) =>
          state.kind === "succeeded"
            ? Deferred.succeed(oldDeliveryStarted, undefined).pipe(
                Effect.andThen(Effect.never),
                Effect.onInterrupt(() => Deferred.succeed(oldDeliveryInterrupted, undefined)),
              )
            : Effect.succeed(false),
        );

        yield* workflow.launch(target, "helper", "workflow", { _tag: "Workflow" });
        yield* Deferred.succeed(launched, {
          key: "ws-1:helper",
          workspace_id: "ws-1",
          target_key: "helper",
          label: "Helper",
          kind: "agent",
          status: "running",
          created_at: "2026-08-05T00:00:00Z",
          display_region: "workflow",
        });
        yield* Deferred.await(oldDeliveryStarted);
        yield* workflow.claimPresenter(target, "replacement", (state) =>
          state.kind === "succeeded"
            ? Deferred.succeed(replacementDelivered, undefined).pipe(Effect.as(true))
            : Effect.succeed(false),
        );
        yield* Deferred.await(replacementDelivered);
        yield* Effect.yieldNow;

        assert.isTrue(Option.isSome(yield* Deferred.poll(oldDeliveryInterrupted)));
      }),
    ),
  );

  it.effect("does not replay a failed mutation after its initiating presenter leaves", () =>
    Effect.scoped(
      Effect.gen(function* () {
        const failLaunch = yield* Deferred.make<void>();
        const replacementStates: WorkspaceRuntimeMutationState[] = [];
        let settled = false;
        const port: WorkspaceRuntimePort = {
          read: () => Effect.succeed(emptyRuntime),
          launch: () =>
            Deferred.await(failLaunch).pipe(
              Effect.andThen(
                Effect.fail(TransientTransportError.make({ operation: "launch workspace session", cause: "lost" })),
              ),
            ),
          rename: unusedPortMethod,
          stop: unusedPortMethod,
          refresh: unusedPortMethod,
          retry: unusedPortMethod,
          delete: unusedPortMethod,
        };
        const workflow = yield* makeWorkspaceRuntimeWorkflow(port);
        const target = { workspaceId: "ws-1" };
        yield* workflow.claimPresenter(target, "first", () => Effect.succeed(false));
        yield* workflow.launch(target, "helper", "workflow", {
          _tag: "Workflow",
          onSettled: () => {
            settled = true;
          },
        });
        yield* workflow.releasePresenter(target, "first");
        yield* workflow.claimPresenter(target, "replacement", (state) =>
          Effect.sync(() => {
            replacementStates.push(state);
            return state.kind !== "pending";
          }),
        );

        yield* Deferred.succeed(failLaunch, undefined);
        while (!settled) yield* Effect.yieldNow;
        yield* Effect.yieldNow;
        yield* Effect.yieldNow;

        assert.deepStrictEqual(
          replacementStates.map((state) => state.kind),
          ["pending"],
        );
      }),
    ),
  );

  it.effect("delivers a completed refresh only to the replacement presenter", () =>
    Effect.scoped(
      Effect.gen(function* () {
        const refreshed = yield* Deferred.make<WorkspaceDetail>();
        const replacementReceived = yield* Deferred.make<void>();
        const firstStates: WorkspaceRuntimeMutationState[] = [];
        const replacementStates: WorkspaceRuntimeMutationState[] = [];
        const port: WorkspaceRuntimePort = {
          read: () => Effect.succeed(emptyRuntime),
          launch: unusedPortMethod,
          rename: unusedPortMethod,
          stop: unusedPortMethod,
          refresh: () => Deferred.await(refreshed),
          retry: unusedPortMethod,
          delete: unusedPortMethod,
        };
        const workflow = yield* makeWorkspaceRuntimeWorkflow(port);
        const target = { workspaceId: "ws-1" };
        yield* workflow.claimPresenter(target, "first", (state) =>
          Effect.sync(() => {
            firstStates.push(state);
            return false;
          }),
        );

        yield* workflow.refresh(target);
        yield* workflow.releasePresenter(target, "first");
        yield* workflow.claimPresenter(target, "replacement", (state) =>
          Effect.gen(function* () {
            replacementStates.push(state);
            if (state.kind !== "succeeded") return false;
            yield* Deferred.succeed(replacementReceived, undefined);
            return true;
          }),
        );
        yield* Deferred.succeed(refreshed, refreshedWorkspace);
        yield* Deferred.await(replacementReceived);

        assert.deepStrictEqual(
          firstStates.map((state) => state.kind),
          ["pending"],
        );
        assert.deepStrictEqual(
          replacementStates.map((state) => state.kind),
          ["pending", "succeeded"],
        );
        const terminalState = replacementStates.at(-1);
        assert.strictEqual(terminalState?.operation, "Refresh");
      }),
    ),
  );

  it.effect("keeps a completed delete with its designated presenter after route replacement", () =>
    Effect.scoped(
      Effect.gen(function* () {
        const deleted = yield* Deferred.make<Response>();
        const designatedReceived = yield* Deferred.make<void>();
        const firstStates: WorkspaceRuntimeMutationState[] = [];
        const replacementStates: WorkspaceRuntimeMutationState[] = [];
        const port: WorkspaceRuntimePort = {
          read: () => Effect.succeed(emptyRuntime),
          launch: unusedPortMethod,
          rename: unusedPortMethod,
          stop: unusedPortMethod,
          refresh: unusedPortMethod,
          retry: unusedPortMethod,
          delete: () => Deferred.await(deleted).pipe(Effect.map((response) => ({ response }))),
        };
        const workflow = yield* makeWorkspaceRuntimeWorkflow(port);
        const target = { workspaceId: "ws-1" };
        yield* workflow.claimPresenter(target, "first", (state) =>
          Effect.gen(function* () {
            firstStates.push(state);
            if (state.kind !== "succeeded") return false;
            yield* Deferred.succeed(designatedReceived, undefined);
            return true;
          }),
        );

        yield* workflow.delete(target, { force: false, identity: workspaceIdentity, presenterID: "first" });
        yield* workflow.claimPresenter(target, "replacement", (state) =>
          Effect.sync(() => {
            replacementStates.push(state);
            return false;
          }),
        );
        yield* Deferred.succeed(deleted, new Response(null, { status: 204 }));
        yield* Deferred.await(designatedReceived);

        assert.deepStrictEqual(
          firstStates.map((state) => state.kind),
          ["pending", "succeeded"],
        );
        assert.deepStrictEqual(replacementStates, []);
        const terminalState = firstStates.at(-1);
        assert.strictEqual(terminalState?.operation, "Delete");
      }),
    ),
  );

  it.effect("delivers a completed stop only to the replacement presenter", () =>
    Effect.scoped(
      Effect.gen(function* () {
        const stopped = yield* Deferred.make<void>();
        const replacementReceived = yield* Deferred.make<void>();
        const firstStates: WorkspaceRuntimeMutationState[] = [];
        const replacementStates: WorkspaceRuntimeMutationState[] = [];
        const port: WorkspaceRuntimePort = {
          read: () => Effect.succeed(emptyRuntime),
          launch: unusedPortMethod,
          rename: unusedPortMethod,
          stop: () => Deferred.await(stopped),
          refresh: unusedPortMethod,
          retry: unusedPortMethod,
          delete: unusedPortMethod,
        };
        const workflow = yield* makeWorkspaceRuntimeWorkflow(port);
        const target = { workspaceId: "ws-1" };
        yield* workflow.claimPresenter(target, "first", (state) =>
          Effect.sync(() => {
            firstStates.push(state);
            return false;
          }),
        );

        yield* workflow.stop(target, "ws-1:helper");
        yield* workflow.releasePresenter(target, "first");
        yield* workflow.claimPresenter(target, "replacement", (state) =>
          Effect.gen(function* () {
            replacementStates.push(state);
            if (state.kind !== "succeeded") return false;
            yield* Deferred.succeed(replacementReceived, undefined);
            return true;
          }),
        );
        yield* Deferred.succeed(stopped, undefined);
        yield* Deferred.await(replacementReceived);

        assert.deepStrictEqual(
          firstStates.map((state) => state.kind),
          ["pending"],
        );
        assert.deepStrictEqual(
          replacementStates.map((state) => state.kind),
          ["pending", "succeeded"],
        );
        assert.strictEqual(replacementStates.at(-1)?.operation, "Stop");
      }),
    ),
  );

  it.effect("delivers a completed rename only to the replacement presenter", () =>
    Effect.scoped(
      Effect.gen(function* () {
        const renamed = yield* Deferred.make<RuntimeSession>();
        const replacementReceived = yield* Deferred.make<void>();
        const firstStates: WorkspaceRuntimeMutationState[] = [];
        const replacementStates: WorkspaceRuntimeMutationState[] = [];
        const port: WorkspaceRuntimePort = {
          read: () => Effect.succeed(emptyRuntime),
          launch: unusedPortMethod,
          rename: () => Deferred.await(renamed),
          stop: unusedPortMethod,
          refresh: unusedPortMethod,
          retry: unusedPortMethod,
          delete: unusedPortMethod,
        };
        const workflow = yield* makeWorkspaceRuntimeWorkflow(port);
        const target = { workspaceId: "ws-1" };
        yield* workflow.claimPresenter(target, "first", (state) =>
          Effect.sync(() => {
            firstStates.push(state);
            return false;
          }),
        );

        yield* workflow.rename(target, "ws-1:helper", "Renamed helper");
        yield* workflow.releasePresenter(target, "first");
        yield* workflow.claimPresenter(target, "replacement", (state) =>
          Effect.gen(function* () {
            replacementStates.push(state);
            if (state.kind !== "succeeded") return false;
            yield* Deferred.succeed(replacementReceived, undefined);
            return true;
          }),
        );
        yield* Deferred.succeed(renamed, {
          key: "ws-1:helper",
          workspace_id: "ws-1",
          target_key: "helper",
          label: "Renamed helper",
          kind: "agent",
          status: "running",
          created_at: "2026-08-05T00:00:00Z",
          display_region: "workflow",
        });
        yield* Deferred.await(replacementReceived);

        assert.deepStrictEqual(
          firstStates.map((state) => state.kind),
          ["pending"],
        );
        assert.deepStrictEqual(
          replacementStates.map((state) => state.kind),
          ["pending", "succeeded"],
        );
        assert.strictEqual(replacementStates.at(-1)?.operation, "Rename");
      }),
    ),
  );

  it.effect("delivers a completed preset only to the replacement presenter", () =>
    Effect.scoped(
      Effect.gen(function* () {
        const launched = yield* Deferred.make<RuntimeSession>();
        const replacementReceived = yield* Deferred.make<void>();
        const firstStates: WorkspaceRuntimeMutationState[] = [];
        const replacementStates: WorkspaceRuntimeMutationState[] = [];
        const preset: WorkflowPreset = {
          id: "review",
          name: "Review",
          createdAt: "2026-08-05T00:00:00Z",
          updatedAt: "2026-08-05T00:00:00Z",
          sessions: [{ sourceKey: "helper", targetKey: "helper", region: "workflow", label: "Helper" }],
          layout: defaultTerminalLayout(),
        };
        const port: WorkspaceRuntimePort = {
          read: () => Effect.succeed(emptyRuntime),
          launch: () => Deferred.await(launched),
          rename: unusedPortMethod,
          stop: unusedPortMethod,
          refresh: unusedPortMethod,
          retry: unusedPortMethod,
          delete: unusedPortMethod,
        };
        const workflow = yield* makeWorkspaceRuntimeWorkflow(port);
        const target = { workspaceId: "ws-1" };
        yield* workflow.claimPresenter(target, "first", (state) =>
          Effect.sync(() => {
            firstStates.push(state);
            return false;
          }),
        );

        yield* workflow.applyPreset(target, preset);
        yield* workflow.releasePresenter(target, "first");
        yield* workflow.claimPresenter(target, "replacement", (state) =>
          Effect.gen(function* () {
            replacementStates.push(state);
            if (state.kind !== "succeeded") return false;
            yield* Deferred.succeed(replacementReceived, undefined);
            return true;
          }),
        );
        yield* Deferred.succeed(launched, {
          key: "ws-1:helper",
          workspace_id: "ws-1",
          target_key: "helper",
          label: "Helper",
          kind: "agent",
          status: "running",
          created_at: "2026-08-05T00:00:00Z",
          display_region: "workflow",
        });
        yield* Deferred.await(replacementReceived);

        assert.deepStrictEqual(
          firstStates.map((state) => state.kind),
          ["pending"],
        );
        assert.deepStrictEqual(
          replacementStates.map((state) => state.kind),
          ["pending", "succeeded"],
        );
        assert.strictEqual(replacementStates.at(-1)?.operation, "ApplyPreset");
      }),
    ),
  );

  it.effect("restores the route presenter after a one-shot delete presenter acknowledges", () =>
    Effect.scoped(
      Effect.gen(function* () {
        const deleteResponse = yield* Deferred.make<WorkspaceRuntimeDeleteResult>();
        const deleted = yield* Deferred.make<void>();
        const routeReceivedRefresh = yield* Deferred.make<void>();
        const routeStates: WorkspaceRuntimeMutationState[] = [];
        const deleteStates: WorkspaceRuntimeMutationState[] = [];
        const port: WorkspaceRuntimePort = {
          read: () => Effect.succeed(emptyRuntime),
          launch: unusedPortMethod,
          rename: unusedPortMethod,
          stop: unusedPortMethod,
          refresh: () => Effect.never,
          retry: unusedPortMethod,
          delete: () => Deferred.await(deleteResponse),
        };
        const workflow = yield* makeWorkspaceRuntimeWorkflow(port);
        const target = { workspaceId: "ws-1" };
        yield* workflow.claimPresenter(target, "route", (state) =>
          Effect.gen(function* () {
            routeStates.push(state);
            if (state.operation === "Refresh") yield* Deferred.succeed(routeReceivedRefresh, undefined);
            return false;
          }),
        );
        yield* workflow.claimPresenter(
          target,
          "delete",
          (state) =>
            Effect.gen(function* () {
              deleteStates.push(state);
              if (state.kind !== "succeeded") return false;
              yield* Deferred.succeed(deleted, undefined);
              return true;
            }),
          { releaseWhenAcknowledged: true },
        );

        yield* workflow.delete(target, { force: false, presenterID: "delete" });
        yield* workflow.claimPresenter(target, "route-returned", (state) =>
          Effect.gen(function* () {
            routeStates.push(state);
            if (state.operation === "Refresh") yield* Deferred.succeed(routeReceivedRefresh, undefined);
            return false;
          }),
        );
        yield* Deferred.succeed(deleteResponse, { response: new Response(null, { status: 204 }) });
        yield* Effect.yieldNow;
        yield* Effect.yieldNow;
        assert.isTrue(Option.isSome(yield* Deferred.poll(deleted)));
        yield* Effect.yieldNow;
        yield* workflow.refresh(target);
        yield* Deferred.await(routeReceivedRefresh);

        assert.deepStrictEqual(
          deleteStates.map((state) => state.operation),
          ["Delete", "Delete"],
        );
        assert.deepStrictEqual(
          routeStates.map((state) => state.operation),
          ["Refresh"],
        );
      }),
    ),
  );

  it.effect("delivers a completed setup retry only to the replacement presenter", () =>
    Effect.scoped(
      Effect.gen(function* () {
        const retried = yield* Deferred.make<WorkspaceDetail>();
        const replacementReceived = yield* Deferred.make<void>();
        const firstStates: WorkspaceRuntimeMutationState[] = [];
        const replacementStates: WorkspaceRuntimeMutationState[] = [];
        const port: WorkspaceRuntimePort = {
          read: () => Effect.succeed(emptyRuntime),
          launch: unusedPortMethod,
          rename: unusedPortMethod,
          stop: unusedPortMethod,
          refresh: unusedPortMethod,
          retry: () => Deferred.await(retried),
          delete: unusedPortMethod,
        };
        const workflow = yield* makeWorkspaceRuntimeWorkflow(port);
        const target = { workspaceId: "ws-1" };
        yield* workflow.claimPresenter(target, "first", (state) =>
          Effect.sync(() => {
            firstStates.push(state);
            return false;
          }),
        );

        yield* workflow.retrySetup(target);
        yield* workflow.releasePresenter(target, "first");
        yield* workflow.claimPresenter(target, "replacement", (state) =>
          Effect.gen(function* () {
            replacementStates.push(state);
            if (state.kind !== "succeeded") return false;
            yield* Deferred.succeed(replacementReceived, undefined);
            return true;
          }),
        );
        yield* Deferred.succeed(retried, refreshedWorkspace);
        yield* Deferred.await(replacementReceived);

        assert.deepStrictEqual(
          firstStates.map((state) => state.kind),
          ["pending"],
        );
        assert.deepStrictEqual(
          replacementStates.map((state) => state.kind),
          ["pending", "succeeded"],
        );
        assert.strictEqual(replacementStates.at(-1)?.operation, "RetrySetup");
      }),
    ),
  );

  it.effect("recovers a launched session from runtime authority after its response is lost", () =>
    Effect.scoped(
      Effect.gen(function* () {
        const recovered = yield* Deferred.make<WorkspaceRuntimeMutationState>();
        let reads = 0;
        let launches = 0;
        const session: RuntimeSession = {
          key: "ws-1:helper",
          workspace_id: "ws-1",
          target_key: "helper",
          label: "Helper",
          kind: "agent",
          status: "running",
          created_at: "2026-08-05T00:00:00Z",
          display_region: "workflow",
        };
        const port: WorkspaceRuntimePort = {
          read: () => {
            reads += 1;
            return Effect.succeed(reads === 1 ? emptyRuntime : { ...emptyRuntime, sessions: [session] });
          },
          launch: () => {
            launches += 1;
            return Effect.fail(
              TransientTransportError.make({ operation: "launch workspace session", cause: "response lost" }),
            );
          },
          rename: unusedPortMethod,
          stop: unusedPortMethod,
          refresh: unusedPortMethod,
          retry: unusedPortMethod,
          delete: unusedPortMethod,
        };
        const workflow = yield* makeWorkspaceRuntimeWorkflow(port);
        const target = { workspaceId: "ws-1" };
        yield* workflow.claimPresenter(target, "route", (state) =>
          state.kind === "succeeded" ? Deferred.succeed(recovered, state).pipe(Effect.as(true)) : Effect.succeed(false),
        );

        yield* workflow.launch(target, "helper", "workflow", { _tag: "Workflow" });
        const state = yield* Deferred.await(recovered);

        assert.strictEqual(launches, 1);
        assert.strictEqual(state.kind, "succeeded");
        if (state.kind === "succeeded" && state.operation === "Launch") {
          assert.strictEqual(state.session.key, session.key);
        }
      }),
    ),
  );

  it.effect("reconciles a fenced launch before accepting an explicit retry", () =>
    Effect.scoped(
      Effect.gen(function* () {
        const firstOutcome = yield* Deferred.make<void>();
        const recovered = yield* Deferred.make<void>();
        let reads = 0;
        let launches = 0;
        const session: RuntimeSession = {
          key: "ws-1:helper",
          workspace_id: "ws-1",
          target_key: "helper",
          label: "Helper",
          kind: "agent",
          status: "running",
          created_at: "2026-08-05T00:00:00Z",
          display_region: "workflow",
        };
        const port: WorkspaceRuntimePort = {
          read: () => {
            reads += 1;
            if (reads === 1) return Effect.succeed(emptyRuntime);
            if (reads === 2) {
              return Effect.fail(
                TransientTransportError.make({ operation: "load workspace runtime", cause: "offline" }),
              );
            }
            return Effect.succeed({ ...emptyRuntime, sessions: [session] });
          },
          launch: () => {
            launches += 1;
            return Effect.fail(
              TransientTransportError.make({ operation: "launch workspace session", cause: "response lost" }),
            );
          },
          rename: unusedPortMethod,
          stop: unusedPortMethod,
          refresh: unusedPortMethod,
          retry: unusedPortMethod,
          delete: unusedPortMethod,
        };
        const workflow = yield* makeWorkspaceRuntimeWorkflow(port);
        const target = { workspaceId: "ws-1" };
        yield* workflow.claimPresenter(target, "route", (state) =>
          Effect.gen(function* () {
            if (state.kind === "pending") return false;
            if (state.kind === "succeeded") yield* Deferred.succeed(recovered, undefined);
            else yield* Deferred.succeed(firstOutcome, undefined);
            return true;
          }),
        );

        yield* workflow.launch(target, "helper", "workflow", { _tag: "Workflow" });
        yield* Deferred.await(firstOutcome);
        yield* workflow.launch(target, "helper", "workflow", { _tag: "Workflow" });
        yield* Deferred.await(recovered);

        assert.strictEqual(launches, 1);
      }),
    ),
  );

  it.effect("recovers rename and stop outcomes from runtime authority", () =>
    Effect.scoped(
      Effect.gen(function* () {
        const renamed = yield* Deferred.make<void>();
        const stopped = yield* Deferred.make<void>();
        let runtime: WorkspaceRuntimeState = {
          ...emptyRuntime,
          sessions: [
            {
              key: "ws-1:helper",
              workspace_id: "ws-1",
              target_key: "helper",
              label: "Renamed helper",
              kind: "agent",
              status: "running",
              created_at: "2026-08-05T00:00:00Z",
              display_region: "workflow",
            },
          ],
        };
        const port: WorkspaceRuntimePort = {
          read: () => Effect.succeed(runtime),
          launch: unusedPortMethod,
          rename: () =>
            Effect.fail(
              TransientTransportError.make({ operation: "rename workspace session", cause: "response lost" }),
            ),
          stop: () => {
            runtime = emptyRuntime;
            return Effect.fail(
              TransientTransportError.make({ operation: "stop workspace session", cause: "response lost" }),
            );
          },
          refresh: unusedPortMethod,
          retry: unusedPortMethod,
          delete: unusedPortMethod,
        };
        const workflow = yield* makeWorkspaceRuntimeWorkflow(port);
        const target = { workspaceId: "ws-1" };
        yield* workflow.claimPresenter(target, "route", (state) =>
          Effect.gen(function* () {
            if (state.kind !== "succeeded") return false;
            if (state.operation === "Rename") yield* Deferred.succeed(renamed, undefined);
            if (state.operation === "Stop") yield* Deferred.succeed(stopped, undefined);
            return true;
          }),
        );

        yield* workflow.rename(target, "ws-1:helper", "Renamed helper");
        yield* Deferred.await(renamed);
        yield* Effect.yieldNow;
        yield* workflow.stop(target, "ws-1:helper");
        yield* Deferred.await(stopped);
      }),
    ),
  );

  it.effect("recovers a deleted workspace from authoritative absence", () =>
    Effect.scoped(
      Effect.gen(function* () {
        const deleted = yield* Deferred.make<void>();
        let deleteCalls = 0;
        const port: WorkspaceRuntimePort = {
          read: () => Effect.succeed(emptyRuntime),
          launch: unusedPortMethod,
          rename: unusedPortMethod,
          stop: unusedPortMethod,
          refresh: unusedPortMethod,
          retry: unusedPortMethod,
          delete: () => {
            deleteCalls += 1;
            return Effect.fail(TransientTransportError.make({ operation: "delete workspace", cause: "response lost" }));
          },
          isDeleted: () => Effect.succeed(true),
        };
        const workflow = yield* makeWorkspaceRuntimeWorkflow(port);
        const target = { workspaceId: "ws-1" };
        yield* workflow.claimPresenter(target, "delete", (state) =>
          state.kind === "succeeded" && state.operation === "Delete"
            ? Deferred.succeed(deleted, undefined).pipe(Effect.as(true))
            : Effect.succeed(false),
        );

        yield* workflow.delete(target, { force: false, presenterID: "delete" });
        yield* Deferred.await(deleted);

        assert.strictEqual(deleteCalls, 1);
      }),
    ),
  );

  it.effect("resumes a partially applied preset without relaunching completed sessions", () =>
    Effect.scoped(
      Effect.gen(function* () {
        const firstOutcome = yield* Deferred.make<void>();
        const recovered = yield* Deferred.make<Readonly<Record<string, string>>>();
        const launchTargets: string[] = [];
        let authorityRecovered = false;
        const alpha: RuntimeSession = {
          key: "ws-1:alpha",
          workspace_id: "ws-1",
          target_key: "alpha",
          label: "Alpha",
          kind: "agent",
          status: "running",
          created_at: "2026-08-05T00:00:00Z",
          display_region: "workflow",
        };
        const beta: RuntimeSession = {
          key: "ws-1:beta",
          workspace_id: "ws-1",
          target_key: "beta",
          label: "Beta",
          kind: "agent",
          status: "running",
          created_at: "2026-08-05T00:00:01Z",
          display_region: "workflow",
        };
        const preset: WorkflowPreset = {
          id: "review",
          name: "Review",
          createdAt: "2026-08-05T00:00:00Z",
          updatedAt: "2026-08-05T00:00:00Z",
          sessions: [
            { sourceKey: "alpha-source", targetKey: "alpha", region: "workflow", label: "Alpha" },
            { sourceKey: "beta-source", targetKey: "beta", region: "workflow", label: "Beta" },
          ],
          layout: defaultTerminalLayout(),
        };
        const port: WorkspaceRuntimePort = {
          read: () =>
            authorityRecovered
              ? Effect.succeed({ ...emptyRuntime, sessions: [alpha, beta] })
              : launchTargets.length < 2
                ? Effect.succeed({ ...emptyRuntime, sessions: launchTargets.length === 0 ? [] : [alpha] })
                : Effect.fail(TransientTransportError.make({ operation: "load workspace runtime", cause: "offline" })),
          launch: (_target, targetKey) => {
            launchTargets.push(targetKey);
            return targetKey === "alpha"
              ? Effect.succeed(alpha)
              : Effect.fail(
                  TransientTransportError.make({ operation: "launch workspace session", cause: "response lost" }),
                );
          },
          rename: unusedPortMethod,
          stop: unusedPortMethod,
          refresh: unusedPortMethod,
          retry: unusedPortMethod,
          delete: unusedPortMethod,
        };
        const workflow = yield* makeWorkspaceRuntimeWorkflow(port);
        const target = { workspaceId: "ws-1" };
        yield* workflow.claimPresenter(target, "route", (state) =>
          Effect.gen(function* () {
            if (state.kind === "pending") return false;
            if (state.kind === "succeeded" && state.operation === "ApplyPreset") {
              yield* Deferred.succeed(recovered, state.keyMap);
            } else {
              yield* Deferred.succeed(firstOutcome, undefined);
            }
            return true;
          }),
        );

        yield* workflow.applyPreset(target, preset);
        yield* Deferred.await(firstOutcome);
        authorityRecovered = true;
        yield* workflow.applyPreset(target, preset);
        const keyMap = yield* Deferred.await(recovered);

        assert.deepStrictEqual(launchTargets, ["alpha", "beta"]);
        assert.deepStrictEqual(keyMap, {
          "alpha-source": "ws-1:alpha",
          "beta-source": "ws-1:beta",
        });
      }),
    ),
  );
});
