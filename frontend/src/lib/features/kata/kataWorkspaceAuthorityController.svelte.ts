import {
  readKataEventStream,
  type KataTaskEventStreamFrame,
  type ReadKataEventStreamOptions,
} from "../../api/kata/eventStream.js";
import type { KataWorkspaceSnapshotProjection } from "../../api/kata/snapshotProjection.js";
import { createKataAuthorityStore, type KataAuthorityStore } from "../../stores/kata-authority.svelte.js";
import { createKataEventStreamController, type KataEventStreamController } from "./kataEventStreamController.js";
import { shouldReloadKataWorkspaceForFrame, type KataWorkspaceAuthorityRequest } from "./kataWorkspaceAuthority.js";

type SnapshotAcceptanceSource = "load" | "retry" | "frame";

export interface KataWorkspaceSnapshotAcceptanceContext {
  source: SnapshotAcceptanceSource;
  frame?: KataTaskEventStreamFrame | undefined;
}

export interface CreateKataWorkspaceAuthorityControllerOptions {
  authorityStore?: KataAuthorityStore | undefined;
  readEventStream?: ((options: ReadKataEventStreamOptions) => Promise<void>) | undefined;
  onSnapshotAccepted?: (
    snapshot: KataWorkspaceSnapshotProjection,
    context: KataWorkspaceSnapshotAcceptanceContext,
  ) => void | Promise<void>;
  resetIssueExpansion?: (() => void) | undefined;
  onStreamOpen?: (() => void) | undefined;
  onStreamError?: ((message: string) => void) | undefined;
  reconnectDelayMS?: number | undefined;
  reconnectMaxDelayMS?: number | undefined;
}

export class KataWorkspaceAuthorityController {
  readonly authorityStore: KataAuthorityStore;
  streamConnected = $state(false);
  streamError = $state<string | null>(null);

  private readonly eventStream: KataEventStreamController;
  private readonly onSnapshotAccepted:
    | ((
        snapshot: KataWorkspaceSnapshotProjection,
        context: KataWorkspaceSnapshotAcceptanceContext,
      ) => void | Promise<void>)
    | undefined;
  private readonly resetIssueExpansion: (() => void) | undefined;
  private desiredRequest: KataWorkspaceAuthorityRequest | null = null;
  private lifecycleGeneration = 0;
  private acceptedResetPending = false;

  constructor(options: CreateKataWorkspaceAuthorityControllerOptions = {}) {
    this.authorityStore = options.authorityStore ?? createKataAuthorityStore();
    this.onSnapshotAccepted = options.onSnapshotAccepted;
    this.resetIssueExpansion = options.resetIssueExpansion;
    this.eventStream = createKataEventStreamController({
      getDaemonId: () => this.desiredDaemonID(),
      getLastEventID: () => this.authorityStore.snapshot?.event_cursor ?? 0,
      onOpen: () => {
        this.streamConnected = true;
        this.streamError = null;
        options.onStreamOpen?.();
      },
      onMessage: async (frame) => this.reloadForFrame(frame),
      onReset: () => {
        if (!this.acceptedResetPending) return;
        this.acceptedResetPending = false;
        this.resetIssueExpansion?.();
      },
      onError: (message) => {
        this.streamConnected = false;
        this.streamError = message;
        options.onStreamError?.(message);
      },
      readEventStream: options.readEventStream ?? readKataEventStream,
      ...(options.reconnectDelayMS === undefined ? {} : { reconnectDelayMS: options.reconnectDelayMS }),
      ...(options.reconnectMaxDelayMS === undefined ? {} : { reconnectMaxDelayMS: options.reconnectMaxDelayMS }),
    });
  }

  async load(request: KataWorkspaceAuthorityRequest): Promise<boolean> {
    const generation = ++this.lifecycleGeneration;
    this.stopStream();
    this.desiredRequest = request;
    this.authorityStore.updatePresentation(request.presentation);

    const accepted = await this.authorityStore.loadSnapshot(request.intent);
    if (!accepted || generation !== this.lifecycleGeneration) return false;
    await this.publishAcceptedSnapshot({ source: "load" });
    if (generation !== this.lifecycleGeneration) return false;
    this.eventStream.start();
    return true;
  }

  async retry(): Promise<boolean> {
    const generation = ++this.lifecycleGeneration;
    this.stopStream();
    const accepted = await this.authorityStore.retry();
    if (!accepted || generation !== this.lifecycleGeneration) return false;
    await this.publishAcceptedSnapshot({ source: "retry" });
    if (generation !== this.lifecycleGeneration) return false;
    this.eventStream.start();
    return true;
  }

  stop(): void {
    this.lifecycleGeneration += 1;
    this.stopStream();
  }

  private stopStream(): void {
    this.acceptedResetPending = false;
    this.streamConnected = false;
    this.eventStream.stop();
  }

  private desiredDaemonID(): string | undefined {
    return (
      this.desiredRequest?.intent.daemon_id ??
      this.authorityStore.state.intent?.daemon_id ??
      this.authorityStore.snapshot?.daemon_id
    );
  }

  private async reloadForFrame(frame: KataTaskEventStreamFrame): Promise<void> {
    this.acceptedResetPending = false;
    if (!shouldReloadKataWorkspaceForFrame(frame, this.authorityStore.snapshot, this.desiredDaemonID())) return;

    const generation = this.lifecycleGeneration;
    let accepted: boolean;
    try {
      accepted = await this.authorityStore.retry();
    } catch {
      return;
    }
    if (!accepted || generation !== this.lifecycleGeneration) return;
    await this.publishAcceptedSnapshot({ source: "frame", frame });
    if (generation !== this.lifecycleGeneration) return;
    this.acceptedResetPending = frame.kind === "reset";
  }

  private async publishAcceptedSnapshot(context: KataWorkspaceSnapshotAcceptanceContext): Promise<void> {
    const snapshot = this.authorityStore.snapshot;
    if (!snapshot) return;
    await this.onSnapshotAccepted?.(snapshot, context);
  }
}

export function createKataWorkspaceAuthorityController(
  options: CreateKataWorkspaceAuthorityControllerOptions = {},
): KataWorkspaceAuthorityController {
  return new KataWorkspaceAuthorityController(options);
}
