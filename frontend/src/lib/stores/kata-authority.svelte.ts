import {
  type KataAuthority,
  type KataAuthorityScope,
  type KataSnapshotIntent,
  type KataWorkspaceSnapshotResponse,
} from "../api/kata/snapshot.js";
import {
  normalizeKataWorkspaceSnapshot,
  type KataWorkspaceSnapshotProjection,
} from "../api/kata/snapshotProjection.js";

export interface KataAuthorityKey {
  daemon_id: string;
  scope: KataAuthorityScope;
  project_uid?: string | undefined;
  authority: KataAuthority;
}

export interface KataAuthorityPresentation {
  text: string;
  owner: string;
  label: string;
}

type KataSnapshotIssue = KataWorkspaceSnapshotProjection["issues"][number];

export interface KataAuthorityProjection {
  issues: readonly KataSnapshotIssue[];
}

export type KataAuthorityState =
  | { phase: "idle"; snapshot: null; intent: null; error: null }
  | { phase: "loading"; snapshot: KataWorkspaceSnapshotProjection | null; intent: KataSnapshotIntent; error: null }
  | {
      phase: "accepted";
      snapshot: KataWorkspaceSnapshotProjection;
      intent: KataSnapshotIntent;
      error: null;
    }
  | {
      phase: "degraded";
      snapshot: KataWorkspaceSnapshotProjection | null;
      intent: KataSnapshotIntent;
      error: string;
    }
  | {
      phase: "abandoned";
      snapshot: null;
      intent: null;
      error: string;
    };

const initialState: KataAuthorityState = { phase: "idle", snapshot: null, intent: null, error: null };
const initialPresentation: KataAuthorityPresentation = {
  text: "",
  owner: "",
  label: "",
};

function optionalValue(value: string | undefined): string | undefined {
  const trimmed = value?.trim();
  return trimmed || undefined;
}

function normalizeIntent(intent: KataSnapshotIntent): KataSnapshotIntent {
  const daemonID = optionalValue(intent.daemon_id);
  const projectUID = optionalValue(intent.project_uid);
  const selectedIssueUID = optionalValue(intent.selected_issue_uid);
  const graphSourceUID = optionalValue(intent.graph_source_uid);
  if (intent.scope === "project" && !projectUID) {
    throw new Error("project_uid is required for project scope");
  }
  if (intent.scope === "global" && projectUID) {
    throw new Error("project_uid is only valid for project scope");
  }
  return {
    ...(daemonID ? { daemon_id: daemonID } : {}),
    scope: intent.scope,
    ...(projectUID ? { project_uid: projectUID } : {}),
    authority: intent.authority,
    ...(selectedIssueUID ? { selected_issue_uid: selectedIssueUID } : {}),
    ...(graphSourceUID ? { graph_source_uid: graphSourceUID } : {}),
  };
}

function authorityIdentityMatches(response: KataWorkspaceSnapshotProjection, intent: KataSnapshotIntent): boolean {
  if (intent.daemon_id && response.daemon_id !== intent.daemon_id) return false;
  if (response.intent.scope !== intent.scope) return false;
  const responseProjectUID = response.intent.scope === "project" ? response.intent.project_uid : undefined;
  if (responseProjectUID !== intent.project_uid) return false;
  if (response.intent.authority !== intent.authority) return false;

  return true;
}

function responseIdentityMatches(response: KataWorkspaceSnapshotProjection, intent: KataSnapshotIntent): boolean {
  if (!authorityIdentityMatches(response, intent)) return false;

  if (response.selected_issue_uid && response.selected_issue_uid !== intent.selected_issue_uid) return false;
  if (response.graph_source_uid !== intent.graph_source_uid) return false;
  return true;
}

function authorityKey(response: KataWorkspaceSnapshotProjection): KataAuthorityKey {
  return {
    daemon_id: response.daemon_id,
    scope: response.intent.scope,
    ...(response.intent.scope === "project" ? { project_uid: response.intent.project_uid } : {}),
    authority: response.intent.authority,
  };
}

function orderingKey(response: KataWorkspaceSnapshotProjection): string {
  const key = authorityKey(response);
  return JSON.stringify([response.server_instance_id, key.daemon_id, key.scope, key.project_uid ?? "", key.authority]);
}

function intentsEqual(left: KataSnapshotIntent | null, right: KataSnapshotIntent): boolean {
  if (!left) return false;
  return (
    left.daemon_id === right.daemon_id &&
    left.scope === right.scope &&
    left.project_uid === right.project_uid &&
    left.authority === right.authority &&
    left.selected_issue_uid === right.selected_issue_uid &&
    left.graph_source_uid === right.graph_source_uid
  );
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

function projectSnapshot(
  snapshot: KataWorkspaceSnapshotProjection | null,
  presentation: KataAuthorityPresentation,
): KataAuthorityProjection {
  const text = presentation.text.trim().toLocaleLowerCase();
  const owner = presentation.owner.trim().toLocaleLowerCase();
  const label = presentation.label.trim().toLocaleLowerCase();
  const issues = (snapshot?.issues ?? []).filter((issue) => {
    if (!snapshot?.member_issue_uid_set.has(issue.uid)) return false;
    if (owner && issue.owner?.toLocaleLowerCase() !== owner) return false;
    if (label && !issue.labels?.some((item) => item.toLocaleLowerCase() === label)) return false;
    if (!text) return true;
    return [issue.title, issue.body, issue.qualified_id, issue.project_name, issue.owner, ...(issue.labels ?? [])].some(
      (value) => value?.toLocaleLowerCase().includes(text),
    );
  });
  return { issues };
}

export class KataAuthorityStore {
  state = $state.raw<KataAuthorityState>(initialState);
  presentation = $state.raw<KataAuthorityPresentation>(initialPresentation);
  projection = $derived.by(() => projectSnapshot(this.state.snapshot, this.presentation));

  private acceptedIntent: KataSnapshotIntent | null = null;
  private acceptedGenerations: Record<string, number> = {};

  get snapshot(): KataWorkspaceSnapshotProjection | null {
    return this.state.snapshot;
  }

  get authorityKey(): KataAuthorityKey | null {
    return this.state.phase !== "abandoned" && this.state.snapshot ? authorityKey(this.state.snapshot) : null;
  }

  updatePresentation(next: Partial<KataAuthorityPresentation>): void {
    this.presentation = { ...this.presentation, ...next };
  }

  abandon(message: string): void {
    this.acceptedIntent = null;
    this.state = {
      phase: "abandoned",
      snapshot: null,
      intent: null,
      error: message,
    };
  }

  beginLoad(requestedIntent: KataSnapshotIntent): KataSnapshotIntent {
    const intent = normalizeIntent(requestedIntent);
    const currentSnapshot = this.state.snapshot;
    const previousSnapshot =
      currentSnapshot && authorityIdentityMatches(currentSnapshot, intent) ? currentSnapshot : null;
    this.state = { phase: "loading", snapshot: previousSnapshot, intent, error: null };
    return intent;
  }

  acceptSnapshot(intent: KataSnapshotIntent, response: KataWorkspaceSnapshotResponse): boolean {
    if (!intentsEqual(this.state.intent, intent)) return false;
    const previousSnapshot = this.state.snapshot;
    let snapshot: KataWorkspaceSnapshotProjection;
    try {
      snapshot = normalizeKataWorkspaceSnapshot(response);
    } catch (error) {
      this.failSnapshot(intent, error);
      throw error;
    }
    if (!responseIdentityMatches(snapshot, intent)) {
      const error = new Error("Kata snapshot response does not match the current request intent");
      this.failSnapshot(intent, error);
      throw error;
    }

    const key = orderingKey(snapshot);
    const acceptedGeneration = this.acceptedGenerations[key];
    if (acceptedGeneration !== undefined && snapshot.generation < acceptedGeneration) {
      if (!intentsEqual(this.acceptedIntent, intent)) {
        this.state = {
          phase: "degraded",
          snapshot: previousSnapshot,
          intent,
          error: "Kata snapshot generation moved backwards for the requested authority",
        };
        return false;
      }
      this.restoreAcceptedState(previousSnapshot);
      return false;
    }

    this.acceptedGenerations[key] = snapshot.generation;
    this.acceptedIntent = intent;
    this.state = { phase: "accepted", snapshot, intent, error: null };
    return true;
  }

  failSnapshot(intent: KataSnapshotIntent, error: unknown): void {
    if (!intentsEqual(this.state.intent, intent)) return;
    this.state = {
      phase: "degraded",
      snapshot: this.state.snapshot,
      intent,
      error: errorMessage(error),
    };
  }

  retryIntent(): KataSnapshotIntent | null {
    return this.state.intent;
  }

  private restoreAcceptedState(snapshot: KataWorkspaceSnapshotProjection | null): void {
    if (snapshot && this.acceptedIntent) {
      this.state = { phase: "accepted", snapshot, intent: this.acceptedIntent, error: null };
      return;
    }
    this.state = initialState;
  }
}

export function createKataAuthorityStore(): KataAuthorityStore {
  return new KataAuthorityStore();
}
