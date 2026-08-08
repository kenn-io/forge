import { Effect } from "effect";
import type { GeneratedApi } from "../../api/generated-api.js";
import type { TransientTransportError } from "../../api/effect-errors.js";
import {
  fetchKataWorkspaceSnapshot,
  type KataSnapshotAPIError,
  type KataSnapshotIntent,
  type KataWorkspaceSnapshotResponse,
} from "../../api/kata/snapshot.js";
import type { KataTaskEventStreamFrame } from "../../api/kata/schemas.js";
import type { KataWorkspaceSnapshotProjection } from "../../api/kata/snapshotProjection.js";
import { createKataAuthorityStore, type KataAuthorityStore } from "../../stores/kata-authority.svelte.js";
import { createKataEventStreamController, type KataEventStreamController } from "./kataEventStreamController.js";
import { KataWorkflow, type KataAuthorityError, type KataWorkflowService } from "./kata-workflow.js";
import { shouldReloadKataWorkspaceForFrame, type KataWorkspaceAuthorityRequest } from "./kataWorkspaceAuthority.js";

type SnapshotAcceptanceSource = "load" | "retry" | "frame";

let nextKataWorkspaceAuthorityOwner = 0;

export function createKataWorkspaceAuthorityOwner(): string {
  nextKataWorkspaceAuthorityOwner += 1;
  return `kata-workspace:${nextKataWorkspaceAuthorityOwner}`;
}

export interface KataWorkspaceSnapshotAcceptanceContext {
  source: SnapshotAcceptanceSource;
  frame?: KataTaskEventStreamFrame | undefined;
}

export interface CreateKataWorkspaceAuthorityControllerOptions {
  owner: string;
  authorityStore?: KataAuthorityStore | undefined;
  loadSnapshot?: KataSnapshotLoader | undefined;
  onSnapshotAccepted?: (
    snapshot: KataWorkspaceSnapshotProjection,
    context: KataWorkspaceSnapshotAcceptanceContext,
  ) => Effect.Effect<void, never, GeneratedApi>;
  resetIssueExpansion?: (() => void) | undefined;
  onStreamOpen?: (() => void) | undefined;
  onStreamError?: ((message: string) => void) | undefined;
}

export type KataSnapshotLoadError = KataSnapshotAPIError | TransientTransportError;
export type KataSnapshotLoader = (
  intent: KataSnapshotIntent,
) => Effect.Effect<KataWorkspaceSnapshotResponse, KataSnapshotLoadError, GeneratedApi>;
export type KataAuthorityLoadError = KataSnapshotLoadError | KataAuthorityError;

export class KataWorkspaceAuthorityController {
  readonly authorityStore: KataAuthorityStore;
  streamConnected = $state(false);
  streamError = $state<string | null>(null);

  private readonly eventStream: KataEventStreamController;
  private readonly owner: string;
  private readonly loadSnapshot: KataSnapshotLoader;
  private readonly onSnapshotAccepted:
    | ((
        snapshot: KataWorkspaceSnapshotProjection,
        context: KataWorkspaceSnapshotAcceptanceContext,
      ) => Effect.Effect<void, never, GeneratedApi>)
    | undefined;
  private readonly resetIssueExpansion: (() => void) | undefined;
  private desiredRequest: KataWorkspaceAuthorityRequest | null = null;
  private active = true;

  constructor(options: CreateKataWorkspaceAuthorityControllerOptions) {
    this.authorityStore = options.authorityStore ?? createKataAuthorityStore();
    this.owner = options.owner;
    this.loadSnapshot = options.loadSnapshot ?? fetchKataWorkspaceSnapshot;
    this.onSnapshotAccepted = options.onSnapshotAccepted;
    this.resetIssueExpansion = options.resetIssueExpansion;
    this.eventStream = createKataEventStreamController({
      owner: options.owner,
      getDaemonId: () => this.desiredDaemonID(),
      getLastEventID: () => this.authorityStore.snapshot?.event_cursor ?? 0,
      onOpen: () => {
        if (!this.active) return;
        this.streamConnected = true;
        this.streamError = null;
        options.onStreamOpen?.();
      },
      onMessage: (frame, workflow) => this.reloadForFrame(frame, workflow),
      onReset: () => this.resetIssueExpansion?.(),
      onError: (message) => {
        if (!this.active) return;
        this.streamConnected = false;
        this.streamError = message;
        options.onStreamError?.(message);
      },
    });
  }

  load(
    request: KataWorkspaceAuthorityRequest,
  ): Effect.Effect<boolean, KataAuthorityLoadError, KataWorkflow | GeneratedApi> {
    return Effect.gen(
      function* (this: KataWorkspaceAuthorityController) {
        if (!this.active) return false;
        yield* this.stopStream();
        this.desiredRequest = request;
        this.authorityStore.updatePresentation(request.presentation);
        return yield* this.loadIntent(request.intent, this.acceptedEffects({ source: "load" }, true));
      }.bind(this),
    );
  }

  retry(): Effect.Effect<boolean, KataAuthorityLoadError, KataWorkflow | GeneratedApi> {
    return Effect.gen(
      function* (this: KataWorkspaceAuthorityController) {
        if (!this.active) return false;
        yield* this.stopStream();
        const intent = this.authorityStore.retryIntent();
        if (!intent) return false;
        return yield* this.loadIntent(intent, this.acceptedEffects({ source: "retry" }, true));
      }.bind(this),
    );
  }

  stop(): Effect.Effect<void, never, KataWorkflow> {
    return Effect.gen(
      function* (this: KataWorkspaceAuthorityController) {
        yield* this.stopStream();
        const workflow = yield* KataWorkflow;
        yield* workflow.interruptAuthority(this.owner);
      }.bind(this),
    );
  }

  dispose(): Effect.Effect<void, never, KataWorkflow> {
    this.active = false;
    return this.stop();
  }

  private stopStream(): Effect.Effect<void, never, KataWorkflow> {
    return this.eventStream.stop.pipe(
      Effect.tap(() =>
        Effect.sync(() => {
          this.streamConnected = false;
        }),
      ),
    );
  }

  private desiredDaemonID(): string | undefined {
    return (
      this.desiredRequest?.intent.daemon_id ??
      this.authorityStore.state.intent?.daemon_id ??
      this.authorityStore.snapshot?.daemon_id
    );
  }

  private reloadForFrame(
    frame: KataTaskEventStreamFrame,
    workflow: KataWorkflowService,
  ): Effect.Effect<boolean, never, GeneratedApi> {
    if (!this.active) return Effect.succeed(false);
    if (!shouldReloadKataWorkspaceForFrame(frame, this.authorityStore.snapshot, this.desiredDaemonID())) {
      return Effect.succeed(false);
    }
    const intent = this.authorityStore.retryIntent();
    if (!intent) return Effect.succeed(false);
    return workflow
      .latestSnapshot(
        this.owner,
        this.authorityStore,
        intent,
        this.loadSnapshot(intent),
        this.publishAcceptedSnapshot({ source: "frame", frame }),
      )
      .pipe(Effect.catch(() => Effect.succeed(false)));
  }

  private loadIntent(
    intent: KataSnapshotIntent,
    onAccepted: Effect.Effect<void, never, KataWorkflow | GeneratedApi>,
  ): Effect.Effect<boolean, KataAuthorityLoadError, KataWorkflow | GeneratedApi> {
    return Effect.gen(
      function* (this: KataWorkspaceAuthorityController) {
        const workflow = yield* KataWorkflow;
        return yield* workflow.latestSnapshot(
          this.owner,
          this.authorityStore,
          intent,
          this.loadSnapshot(intent),
          onAccepted,
        );
      }.bind(this),
    );
  }

  private publishAcceptedSnapshot(
    context: KataWorkspaceSnapshotAcceptanceContext,
  ): Effect.Effect<void, never, GeneratedApi> {
    return Effect.suspend(() => {
      if (!this.active) return Effect.void;
      const snapshot = this.authorityStore.snapshot;
      if (!snapshot || !this.onSnapshotAccepted) return Effect.void;
      return this.onSnapshotAccepted(snapshot, context);
    });
  }

  private acceptedEffects(
    context: KataWorkspaceSnapshotAcceptanceContext,
    startStream: boolean,
  ): Effect.Effect<void, never, KataWorkflow | GeneratedApi> {
    return Effect.suspend(() => {
      if (!this.active) return Effect.void;
      return this.publishAcceptedSnapshot(context).pipe(
        Effect.andThen(Effect.suspend(() => (this.active && startStream ? this.eventStream.start : Effect.void))),
      );
    });
  }
}

export function createKataWorkspaceAuthorityController(
  options: CreateKataWorkspaceAuthorityControllerOptions,
): KataWorkspaceAuthorityController {
  return new KataWorkspaceAuthorityController(options);
}
