import type { ReadKataEventStreamOptions } from "../../api/kata/eventStream.js";
import {
  fetchKataWorkspaceSnapshot,
  type KataSnapshotIntent,
  type KataWorkspaceSnapshotResponse,
} from "../../api/kata/snapshot.js";
import type { Immutable, KataWorkspaceSnapshotProjection } from "../../api/kata/snapshotProjection.js";
import type { KataTaskDetail, KataTaskSummary } from "../../api/kata/taskTypes.js";
import { createKataAuthorityStore } from "../../stores/kata-authority.svelte.js";
import { createKataWorkspaceAuthorityController } from "./kataWorkspaceAuthorityController.svelte.js";

type SnapshotIssue = KataWorkspaceSnapshotProjection["issues"][number];

export type KataAuxiliaryIssue = Immutable<
  Pick<
    KataTaskSummary,
    | "uid"
    | "short_id"
    | "qualified_id"
    | "title"
    | "body"
    | "project_uid"
    | "project_name"
    | "status"
    | "owner"
    | "labels"
    | "metadata"
  >
>;

export interface KataAuxiliaryAuthoritySource {
  readonly issues: readonly KataAuxiliaryIssue[];
  readonly daemonID: string | undefined;
  readonly phase: "idle" | "loading" | "accepted" | "degraded" | "abandoned";
  readonly error: string | null;
  retry(): Promise<boolean>;
}

export type KataSelectedIssueDetail = Immutable<KataTaskDetail>;

export interface KataSelectedIssueSelection {
  readonly daemonID: string;
  readonly detail: KataSelectedIssueDetail;
}

export interface KataSelectedIssueAuthority {
  selectIssue(issueUID: string, daemonID?: string): Promise<KataSelectedIssueSelection>;
  refreshIssues(daemonID: string): Promise<boolean>;
}

export interface CreateKataAuxiliaryAuthorityOptions {
  loadSnapshot?: ((intent: KataSnapshotIntent) => Promise<KataWorkspaceSnapshotResponse>) | undefined;
  readEventStream?: ((options: ReadKataEventStreamOptions) => Promise<void>) | undefined;
}

function intent(daemonID?: string, selectedIssueUID?: string): KataSnapshotIntent {
  return {
    ...(daemonID?.trim() ? { daemon_id: daemonID.trim() } : {}),
    scope: "global",
    authority: "all",
    ...(selectedIssueUID?.trim() ? { selected_issue_uid: selectedIssueUID.trim() } : {}),
  };
}

export class KataAuxiliaryAuthority implements KataAuxiliaryAuthoritySource, KataSelectedIssueAuthority {
  private readonly controller;
  private readonly loadSnapshot: (intent: KataSnapshotIntent) => Promise<KataWorkspaceSnapshotResponse>;
  private desiredDaemonID: string | undefined;
  private refreshTail: Promise<void> = Promise.resolve();
  private stopped = false;

  constructor(options: CreateKataAuxiliaryAuthorityOptions = {}) {
    this.loadSnapshot = options.loadSnapshot ?? fetchKataWorkspaceSnapshot;
    const authorityStore = createKataAuthorityStore({ loadSnapshot: this.loadSnapshot });
    this.controller = createKataWorkspaceAuthorityController({
      authorityStore,
      ...(options.readEventStream ? { readEventStream: options.readEventStream } : {}),
    });
  }

  get issues(): readonly SnapshotIssue[] {
    return this.controller.authorityStore.snapshot?.issues ?? [];
  }

  get daemonID(): string | undefined {
    return this.controller.authorityStore.snapshot?.daemon_id;
  }

  get phase(): KataAuxiliaryAuthoritySource["phase"] {
    return this.controller.authorityStore.state.phase;
  }

  get error(): string | null {
    return this.controller.authorityStore.state.error;
  }

  async load(daemonID?: string): Promise<boolean> {
    const requestedDaemonID = daemonID?.trim() || undefined;
    this.desiredDaemonID = requestedDaemonID;
    const accepted = await this.controller.load({
      intent: intent(requestedDaemonID),
      presentation: { text: "", owner: "", label: "" },
    });
    if (accepted && this.desiredDaemonID === requestedDaemonID) {
      this.desiredDaemonID = this.controller.authorityStore.snapshot?.daemon_id;
    }
    return accepted;
  }

  async selectIssue(issueUID: string, daemonID?: string): Promise<KataSelectedIssueSelection> {
    const selectedIssueUID = issueUID.trim();
    if (!selectedIssueUID) throw new Error("Kata issue UID is required");
    const selectionStore = createKataAuthorityStore({ loadSnapshot: this.loadSnapshot });
    const accepted = await selectionStore.loadSnapshot(
      intent(daemonID?.trim() || this.desiredDaemonID, selectedIssueUID),
    );
    const snapshot = selectionStore.snapshot;
    if (!accepted || snapshot?.selected_issue_uid !== selectedIssueUID || !snapshot.selected_detail) {
      throw new Error(`Kata snapshot did not include selected task ${selectedIssueUID}`);
    }
    return { daemonID: snapshot.daemon_id, detail: snapshot.selected_detail };
  }

  async refreshIssues(daemonID: string): Promise<boolean> {
    const requestedDaemonID = daemonID.trim();
    if (!requestedDaemonID) return false;

    let accepted = false;
    const refresh = this.refreshTail
      .catch(() => {})
      .then(async () => {
        // A refresh queued behind another one can reach this point after
        // stop(); retrying then would restart the event stream past teardown.
        if (this.stopped) return;
        if (this.desiredDaemonID !== requestedDaemonID) return;
        accepted = await this.controller.retry();
      });
    this.refreshTail = refresh.then(
      () => {},
      () => {},
    );
    await refresh;
    return accepted;
  }

  retry(): Promise<boolean> {
    return this.controller.retry();
  }

  stop(): void {
    this.stopped = true;
    this.controller.stop();
  }
}

export function createKataAuxiliaryAuthority(
  options: CreateKataAuxiliaryAuthorityOptions = {},
): KataAuxiliaryAuthority {
  return new KataAuxiliaryAuthority(options);
}
