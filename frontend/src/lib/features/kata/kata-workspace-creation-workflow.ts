import { Context, Effect, FiberMap, Layer, Ref, Semaphore } from "effect";
import type { Scope } from "effect/Scope";
import { ApiProblemError, InvalidExternalPayload, TransientTransportError } from "../../api/effect-errors.js";
import { executeGeneratedApiRequest, GeneratedApi } from "../../api/generated-api.js";
import type { components } from "../../api/generated/schema.js";
import { createKataWorkspaceForTask, type KataWorkspaceTaskIdentity } from "../../api/kata/workspaces.js";
import { isTransientFailure, transientRetrySchedule } from "../../api/retry-policy.js";
import {
  beginWorkspaceCreate,
  endWorkspaceCreate,
  queueWorkspaceLaunch,
  recordWorkspaceCreated,
} from "../../stores/workspace-create-pending.svelte.js";
import { showFlash } from "../../stores/flash.svelte.js";
import type { WorkspaceItemIdentity } from "../../workspace-inline.js";

export type KataWorkspaceCreationFailure = ApiProblemError | InvalidExternalPayload | TransientTransportError;
type KataWorkspaceRecord = components["schemas"]["WorkspaceResponse"];

export interface KataWorkspaceCreationPort {
  readonly create: (purpose: KataWorkspaceTaskIdentity) => Effect.Effect<void, KataWorkspaceCreationFailure>;
  readonly load: (workspaceID: string) => Effect.Effect<KataWorkspaceRecord, KataWorkspaceCreationFailure>;
  readonly list: () => Effect.Effect<ReadonlyArray<KataWorkspaceRecord>, KataWorkspaceCreationFailure>;
}

export interface KataWorkspaceCreationPresentation {
  readonly isCurrent: () => boolean;
  readonly navigate: (workspaceID: string) => Effect.Effect<void>;
}

export interface KataWorkspaceCreationRequest {
  readonly purpose: KataWorkspaceTaskIdentity;
  readonly itemIdentity: WorkspaceItemIdentity;
  readonly launchTargetKey?: string | undefined;
  readonly presentation: KataWorkspaceCreationPresentation;
}

export interface KataWorkspaceCreationWorkflowService {
  readonly submit: (request: KataWorkspaceCreationRequest) => Effect.Effect<void>;
  readonly workspaceCreated: (
    workspaceID: string,
    created: boolean,
  ) => Effect.Effect<void, KataWorkspaceCreationFailure>;
  readonly workspaceStatus: (workspaceID: string) => Effect.Effect<void, KataWorkspaceCreationFailure>;
  readonly reconcile: Effect.Effect<void, KataWorkspaceCreationFailure>;
}

interface KataWorkspaceCreationWorkflowOptions {
  readonly notify: (message: string, tone: "danger" | "warning" | "default") => Effect.Effect<void>;
}

const defaultOptions: KataWorkspaceCreationWorkflowOptions = {
  notify: (message, tone) => Effect.sync(() => showFlash(message, tone === "default" ? undefined : { tone })),
};

function purposeKey(purpose: KataWorkspaceTaskIdentity): string {
  return JSON.stringify([purpose.daemon_id, purpose.project_uid, purpose.issue_uid]);
}

function workspaceMatchesPurpose(workspace: KataWorkspaceRecord, purpose: KataWorkspaceTaskIdentity): boolean {
  return (
    workspace.item_type === "kata_task" &&
    workspace.kata?.daemon_id === purpose.daemon_id &&
    workspace.kata.project_uid === purpose.project_uid &&
    workspace.kata.issue_uid === purpose.issue_uid
  );
}

function failureMessage(failure: KataWorkspaceCreationFailure): string {
  if (failure instanceof ApiProblemError) return failure.problem.detail ?? "Workspace creation was rejected.";
  if (failure.cause instanceof Error && failure.cause.message !== "") return failure.cause.message;
  return "Workspace creation could not be confirmed.";
}

export function makeKataWorkspaceCreationWorkflow(
  port: KataWorkspaceCreationPort,
  options: KataWorkspaceCreationWorkflowOptions = defaultOptions,
): Effect.Effect<KataWorkspaceCreationWorkflowService, never, Scope> {
  return Effect.gen(function* () {
    const pending = yield* Ref.make<ReadonlyMap<string, KataWorkspaceCreationRequest>>(new Map());
    const awaitingReady = yield* Ref.make<ReadonlyMap<string, KataWorkspaceCreationRequest>>(new Map());
    const workers = yield* FiberMap.make<string>();
    const mutationLock = yield* Semaphore.make(1);

    const removePendingRequest = Effect.fn("KataWorkspaceCreation.removePending")(function* (
      key: string,
      request: KataWorkspaceCreationRequest,
    ) {
      const removed = yield* Ref.modify(
        pending,
        (current): readonly [boolean, ReadonlyMap<string, KataWorkspaceCreationRequest>] => {
          if (current.get(key) !== request) return [false, current];
          const next = new Map(current);
          next.delete(key);
          return [true, next];
        },
      );
      if (removed) endWorkspaceCreate(request.itemIdentity);
      return removed;
    });

    const removeRejectedRequest = Effect.fn("KataWorkspaceCreation.removeRejected")(function* (
      key: string,
      request: KataWorkspaceCreationRequest,
      failure: ApiProblemError,
    ) {
      if (!(yield* removePendingRequest(key, request))) return;
      yield* options.notify(failureMessage(failure), "danger");
    });

    const removeAwaiting = (workspaceID: string): Effect.Effect<void> =>
      Ref.update(awaitingReady, (current) => {
        if (!current.has(workspaceID)) return current;
        const next = new Map(current);
        next.delete(workspaceID);
        return next;
      });

    const publishAccepted = Effect.fn("KataWorkspaceCreation.publishAccepted")(function* (
      request: KataWorkspaceCreationRequest,
      workspace: KataWorkspaceRecord,
      firstConfirmation: boolean,
      created?: boolean | undefined,
    ) {
      recordWorkspaceCreated(request.itemIdentity, { id: workspace.id, status: workspace.status });
      if (workspace.status === "ready") {
        if (request.launchTargetKey) queueWorkspaceLaunch(workspace.id, request.launchTargetKey, undefined);
        yield* removeAwaiting(workspace.id);
      } else if (workspace.status === "error") {
        yield* removeAwaiting(workspace.id);
        yield* options.notify(workspace.error_message ?? "Workspace setup failed.", "danger");
      } else if (request.launchTargetKey) {
        yield* Ref.update(awaitingReady, (current) => new Map(current).set(workspace.id, request));
      }
      if (!firstConfirmation) return;
      if (request.presentation.isCurrent()) {
        yield* request.presentation.navigate(workspace.id);
      } else {
        let message = "Workspace is ready.";
        if (created === true) message = "Workspace created.";
        if (created === false) message = "Workspace already exists.";
        yield* options.notify(message, "default");
      }
    });

    const settleLoaded = Effect.fn("KataWorkspaceCreation.settleLoaded")(function* (
      workspace: KataWorkspaceRecord,
      created?: boolean | undefined,
    ) {
      yield* mutationLock.withPermit(
        Effect.gen(function* () {
          const currentPending = yield* Ref.get(pending);
          const matched = Array.from(currentPending.entries()).find(([, request]) =>
            workspaceMatchesPurpose(workspace, request.purpose),
          );
          if (matched !== undefined) {
            const [key, request] = matched;
            yield* Ref.update(pending, (current) => {
              if (current.get(key) !== request) return current;
              const next = new Map(current);
              next.delete(key);
              return next;
            });
            endWorkspaceCreate(request.itemIdentity);
            yield* publishAccepted(request, workspace, true, created);
            return;
          }
          const awaiting = (yield* Ref.get(awaitingReady)).get(workspace.id);
          if (awaiting !== undefined) yield* publishAccepted(awaiting, workspace, false);
        }),
      );
    });

    const settleWorkspace = Effect.fn("KataWorkspaceCreation.settleWorkspace")(function* (workspaceID: string) {
      const workspace = yield* port.load(workspaceID);
      yield* settleLoaded(workspace);
    });

    const settleCreatedWorkspace = Effect.fn("KataWorkspaceCreation.settleCreatedWorkspace")(function* (
      workspaceID: string,
      created: boolean,
    ) {
      if ((yield* Ref.get(pending)).size === 0) return;
      const workspace = yield* port.load(workspaceID);
      yield* settleLoaded(workspace, created);
    });

    const settleWorkspaceStatus = Effect.fn("KataWorkspaceCreation.settleWorkspaceStatus")(function* (
      workspaceID: string,
    ) {
      if ((yield* Ref.get(pending)).size === 0 && !(yield* Ref.get(awaitingReady)).has(workspaceID)) return;
      yield* settleWorkspace(workspaceID);
    });

    const endUnconfirmedRequest = Effect.fn("KataWorkspaceCreation.endUnconfirmed")(function* (
      key: string,
      request: KataWorkspaceCreationRequest,
    ) {
      if (!(yield* removePendingRequest(key, request))) return;
      yield* options.notify("Workspace creation could not be confirmed. Try again.", "danger");
    });

    const reconcileUncertainCreate = Effect.fn("KataWorkspaceCreation.reconcileUncertain")(function* (
      key: string,
      request: KataWorkspaceCreationRequest,
    ) {
      const matched = yield* port.list().pipe(
        Effect.map((workspaces) => workspaces.find((workspace) => workspaceMatchesPurpose(workspace, request.purpose))),
        Effect.catch(() => Effect.succeed(undefined)),
      );
      if (matched !== undefined) {
        yield* settleLoaded(matched);
        return;
      }
      yield* endUnconfirmedRequest(key, request);
    });

    const runCreate = (key: string, request: KataWorkspaceCreationRequest) =>
      port.create(request.purpose).pipe(
        Effect.retry({ schedule: transientRetrySchedule, while: isTransientFailure }),
        Effect.catchTag("ApiProblemError", (failure) => removeRejectedRequest(key, request, failure)),
        Effect.catchTags({
          InvalidExternalPayload: () => reconcileUncertainCreate(key, request),
          TransientTransportError: () => reconcileUncertainCreate(key, request),
        }),
      );

    const submit = Effect.fn("KataWorkspaceCreation.submit")(function* (request: KataWorkspaceCreationRequest) {
      const key = purposeKey(request.purpose);
      const admitted = yield* Ref.modify(
        pending,
        (current): readonly [boolean, ReadonlyMap<string, KataWorkspaceCreationRequest>] => {
          if (current.has(key)) return [false, current];
          return [true, new Map(current).set(key, request)];
        },
      );
      if (!admitted) return;
      beginWorkspaceCreate(request.itemIdentity);
      yield* FiberMap.run(workers, key, runCreate(key, request));
    });

    const reconcile = Effect.gen(function* () {
      const currentPending = yield* Ref.get(pending);
      const currentAwaiting = yield* Ref.get(awaitingReady);
      if (currentPending.size === 0 && currentAwaiting.size === 0) return;
      const workspaces = yield* port.list();
      yield* Effect.forEach(workspaces, (workspace) => settleLoaded(workspace), { discard: true });
    });

    return {
      submit,
      workspaceCreated: settleCreatedWorkspace,
      workspaceStatus: settleWorkspaceStatus,
      reconcile,
    };
  });
}

class KataWorkspaceCreationPortTag extends Context.Service<KataWorkspaceCreationPortTag, KataWorkspaceCreationPort>()(
  "kenn-forge/KataWorkspaceCreationPort",
) {}

export const KataWorkspaceCreationPortLive = Layer.effect(
  KataWorkspaceCreationPortTag,
  Effect.gen(function* () {
    const api = yield* GeneratedApi;
    return {
      create: (purpose) =>
        createKataWorkspaceForTask(purpose).pipe(Effect.provideService(GeneratedApi, api), Effect.asVoid),
      load: (workspaceID) =>
        executeGeneratedApiRequest("load created Kata workspace", (client, signal) =>
          client.GET("/workspaces/{id}", { params: { path: { id: workspaceID } }, signal }),
        ).pipe(Effect.provideService(GeneratedApi, api)),
      list: () =>
        executeGeneratedApiRequest("reconcile created Kata workspaces", (client, signal) =>
          client.GET("/workspaces", { signal }),
        ).pipe(
          Effect.provideService(GeneratedApi, api),
          Effect.map((response) => response.workspaces ?? []),
        ),
    };
  }),
);

export class KataWorkspaceCreationWorkflow extends Context.Service<
  KataWorkspaceCreationWorkflow,
  KataWorkspaceCreationWorkflowService
>()("kenn-forge/KataWorkspaceCreationWorkflow") {}

export const KataWorkspaceCreationWorkflowLive = Layer.effect(
  KataWorkspaceCreationWorkflow,
  KataWorkspaceCreationPortTag.pipe(Effect.flatMap(makeKataWorkspaceCreationWorkflow)),
).pipe(Layer.provide(KataWorkspaceCreationPortLive));
