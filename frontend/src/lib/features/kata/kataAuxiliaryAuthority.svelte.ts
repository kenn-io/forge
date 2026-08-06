import { Effect } from "effect";
import type { GeneratedApi } from "../../api/generated-api.js";
import { fetchKataWorkspaceSnapshot, type KataSnapshotIntent } from "../../api/kata/snapshot.js";
import type { Immutable, KataWorkspaceSnapshotProjection } from "../../api/kata/snapshotProjection.js";
import type { KataTaskDetail, KataTaskSummary } from "../../api/kata/taskTypes.js";
import { createKataAuthorityStore } from "../../stores/kata-authority.svelte.js";
import { KataAuthorityError, KataWorkflow } from "./kata-workflow.js";
import {
  createKataWorkspaceAuthorityController,
  createKataWorkspaceAuthorityOwner,
} from "./kataWorkspaceAuthorityController.svelte.js";
import type { KataAuthorityLoadError, KataSnapshotLoader } from "./kataWorkspaceAuthorityController.svelte.js";

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
  retry(): Effect.Effect<boolean, KataAuthorityLoadError, GeneratedApi | KataWorkflow>;
}

export type KataSelectedIssueDetail = Immutable<KataTaskDetail>;

export interface KataSelectedIssueSelection {
  readonly daemonID: string;
  readonly detail: KataSelectedIssueDetail;
}

export interface KataSelectedIssueAuthority {
  selectIssue(
    issueUID: string,
    daemonID?: string,
  ): Effect.Effect<KataSelectedIssueSelection, KataAuthorityLoadError, GeneratedApi | KataWorkflow>;
  refreshIssues(daemonID: string): Effect.Effect<boolean, KataAuthorityLoadError, GeneratedApi | KataWorkflow>;
}

export interface CreateKataAuxiliaryAuthorityOptions {
  loadSnapshot?: KataSnapshotLoader | undefined;
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
  private readonly loadSnapshot: KataSnapshotLoader;
  private readonly owner = createKataWorkspaceAuthorityOwner();
  private readonly selectionOwners = new Set<string>();
  private desiredDaemonID: string | undefined;
  private selectionSequence = 0;
  private stopped = false;

  constructor(options: CreateKataAuxiliaryAuthorityOptions) {
    this.loadSnapshot = options.loadSnapshot ?? fetchKataWorkspaceSnapshot;
    const authorityStore = createKataAuthorityStore();
    this.controller = createKataWorkspaceAuthorityController({
      owner: this.owner,
      authorityStore,
      loadSnapshot: this.loadSnapshot,
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

  load(daemonID?: string): Effect.Effect<boolean, KataAuthorityLoadError, GeneratedApi | KataWorkflow> {
    const requestedDaemonID = daemonID?.trim() || undefined;
    return Effect.suspend(() => {
      if (this.stopped) return Effect.succeed(false);
      return Effect.sync(() => {
        this.desiredDaemonID = requestedDaemonID;
      }).pipe(
        Effect.andThen(
          this.controller.load({
            intent: intent(requestedDaemonID),
            presentation: { text: "", owner: "", label: "" },
          }),
        ),
        Effect.tap((accepted) =>
          Effect.sync(() => {
            if (accepted && this.desiredDaemonID === requestedDaemonID) {
              this.desiredDaemonID = this.controller.authorityStore.snapshot?.daemon_id;
            }
          }),
        ),
      );
    });
  }

  selectIssue(
    issueUID: string,
    daemonID?: string,
  ): Effect.Effect<KataSelectedIssueSelection, KataAuthorityLoadError, GeneratedApi | KataWorkflow> {
    const selectedIssueUID = issueUID.trim();
    const selectedIntent = intent(daemonID?.trim() || this.desiredDaemonID, selectedIssueUID);
    const loadSnapshot = this.loadSnapshot;
    const selectionOwner = `${this.owner}:selection:${++this.selectionSequence}`;
    return Effect.gen(
      function* (this: KataAuxiliaryAuthority) {
        if (this.stopped) {
          return yield* Effect.fail(
            KataAuthorityError.make({ message: "Kata auxiliary authority is stopped", cause: new Error("stopped") }),
          );
        }
        if (!selectedIssueUID) {
          return yield* Effect.fail(
            KataAuthorityError.make({ message: "Kata issue UID is required", cause: new Error("missing issue UID") }),
          );
        }
        const selectionStore = createKataAuthorityStore();
        const workflow = yield* KataWorkflow;
        yield* Effect.sync(() => this.selectionOwners.add(selectionOwner));
        const accepted = yield* workflow.latestSnapshot(
          selectionOwner,
          selectionStore,
          selectedIntent,
          loadSnapshot(selectedIntent),
        );
        const snapshot = selectionStore.snapshot;
        if (!accepted || snapshot?.selected_issue_uid !== selectedIssueUID || !snapshot.selected_detail) {
          return yield* Effect.fail(
            KataAuthorityError.make({
              message: `Kata snapshot did not include selected task ${selectedIssueUID}`,
              cause: new Error("selected issue enrichment was absent"),
            }),
          );
        }
        return { daemonID: snapshot.daemon_id, detail: snapshot.selected_detail };
      }.bind(this),
    ).pipe(Effect.ensuring(Effect.sync(() => this.selectionOwners.delete(selectionOwner))));
  }

  refreshIssues(daemonID: string): Effect.Effect<boolean, KataAuthorityLoadError, GeneratedApi | KataWorkflow> {
    const requestedDaemonID = daemonID.trim();
    return Effect.suspend(() => {
      if (this.stopped || !requestedDaemonID || this.desiredDaemonID !== requestedDaemonID) {
        return Effect.succeed(false);
      }
      return this.controller.retry();
    });
  }

  retry(): Effect.Effect<boolean, KataAuthorityLoadError, GeneratedApi | KataWorkflow> {
    return Effect.suspend(() => (this.stopped ? Effect.succeed(false) : this.controller.retry()));
  }

  stop(): Effect.Effect<void, never, KataWorkflow> {
    this.stopped = true;
    this.desiredDaemonID = undefined;
    const selectionOwners = [...this.selectionOwners];
    return Effect.gen(
      function* (this: KataAuxiliaryAuthority) {
        const workflow = yield* KataWorkflow;
        yield* Effect.forEach(selectionOwners, (owner) => workflow.interruptAuthority(owner), { discard: true });
        yield* this.controller.dispose();
      }.bind(this),
    );
  }
}

export function createKataAuxiliaryAuthority(options: CreateKataAuxiliaryAuthorityOptions): KataAuxiliaryAuthority {
  return new KataAuxiliaryAuthority(options);
}
