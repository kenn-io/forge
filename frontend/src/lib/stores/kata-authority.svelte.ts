import {
  fetchKataWorkspaceSnapshot,
  type KataAuthority,
  type KataAuthorityScope,
  type KataSnapshotIntent,
  type KataWorkspaceSnapshotResponse,
} from "../api/kata/snapshot.js";
import type { KataTaskViewName } from "../api/kata/taskTypes.js";

export interface KataAuthorityKey {
  daemon_id: string;
  scope: KataAuthorityScope;
  project_uid?: string | undefined;
  authority: KataAuthority;
}

export interface KataAuthorityPresentation {
  view: KataTaskViewName;
  text: string;
  owner: string;
  label: string;
  graph_selected_uid?: string | undefined;
}

type KataSnapshotIssue = NonNullable<KataWorkspaceSnapshotResponse["issues"]>[number];

export interface KataAuthorityProjection {
  view: KataTaskViewName;
  issues: KataSnapshotIssue[];
  graph_selected_uid?: string | undefined;
}

export type KataAuthorityState =
  | { phase: "idle"; snapshot: null; intent: null; error: null }
  | { phase: "loading"; snapshot: KataWorkspaceSnapshotResponse | null; intent: KataSnapshotIntent; error: null }
  | {
      phase: "accepted";
      snapshot: KataWorkspaceSnapshotResponse;
      intent: KataSnapshotIntent;
      error: null;
    }
  | {
      phase: "degraded";
      snapshot: KataWorkspaceSnapshotResponse | null;
      intent: KataSnapshotIntent;
      error: string;
    };

export interface CreateKataAuthorityStoreOptions {
  loadSnapshot?: ((intent: KataSnapshotIntent) => Promise<KataWorkspaceSnapshotResponse>) | undefined;
}

const initialState: KataAuthorityState = { phase: "idle", snapshot: null, intent: null, error: null };
const initialPresentation: KataAuthorityPresentation = {
  view: "all",
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

function responseIdentityMatches(response: KataWorkspaceSnapshotResponse, intent: KataSnapshotIntent): boolean {
  if (intent.daemon_id && response.daemon_id !== intent.daemon_id) return false;
  if (response.intent.scope !== intent.scope) return false;
  if ((response.intent.project_uid ?? undefined) !== intent.project_uid) return false;
  if (response.intent.authority !== intent.authority) return false;

  const selectedIssueUID = response.enrichment.selected_issue_uid;
  if (selectedIssueUID && selectedIssueUID !== intent.selected_issue_uid) return false;
  if (!intent.selected_issue_uid && selectedIssueUID) return false;

  const graphSourceUID = response.enrichment.graph?.source_uid;
  if (graphSourceUID && graphSourceUID !== intent.graph_source_uid) return false;
  if (!intent.graph_source_uid && graphSourceUID) return false;
  return true;
}

function authorityKey(response: KataWorkspaceSnapshotResponse): KataAuthorityKey {
  return {
    daemon_id: response.daemon_id,
    scope: response.intent.scope as KataAuthorityScope,
    ...(response.intent.project_uid ? { project_uid: response.intent.project_uid } : {}),
    authority: response.intent.authority as KataAuthority,
  };
}

function orderingKey(response: KataWorkspaceSnapshotResponse): string {
  const key = authorityKey(response);
  return JSON.stringify([response.server_instance_id, key.daemon_id, key.scope, key.project_uid ?? "", key.authority]);
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

function projectSnapshot(
  snapshot: KataWorkspaceSnapshotResponse | null,
  presentation: KataAuthorityPresentation,
): KataAuthorityProjection {
  const text = presentation.text.trim().toLocaleLowerCase();
  const owner = presentation.owner.trim().toLocaleLowerCase();
  const label = presentation.label.trim().toLocaleLowerCase();
  const issues = (snapshot?.issues ?? []).filter((issue) => {
    if (owner && issue.owner?.toLocaleLowerCase() !== owner) return false;
    if (label && !issue.labels?.some((item) => item.toLocaleLowerCase() === label)) return false;
    if (!text) return true;
    return [issue.title, issue.body, issue.qualified_id, issue.project_name].some((value) =>
      value.toLocaleLowerCase().includes(text),
    );
  });
  return {
    view: presentation.view,
    issues,
    ...(presentation.graph_selected_uid ? { graph_selected_uid: presentation.graph_selected_uid } : {}),
  };
}

export class KataAuthorityStore {
  state = $state.raw<KataAuthorityState>(initialState);
  presentation = $state.raw<KataAuthorityPresentation>(initialPresentation);
  projection = $derived.by(() => projectSnapshot(this.state.snapshot, this.presentation));

  private readonly load: (intent: KataSnapshotIntent) => Promise<KataWorkspaceSnapshotResponse>;
  private requestSequence = 0;
  private acceptedIntent: KataSnapshotIntent | null = null;
  private acceptedGenerations: Record<string, number> = {};

  constructor(options: CreateKataAuthorityStoreOptions = {}) {
    this.load = options.loadSnapshot ?? fetchKataWorkspaceSnapshot;
  }

  get snapshot(): KataWorkspaceSnapshotResponse | null {
    return this.state.snapshot;
  }

  get authorityKey(): KataAuthorityKey | null {
    return this.state.snapshot ? authorityKey(this.state.snapshot) : null;
  }

  updatePresentation(next: Partial<KataAuthorityPresentation>): void {
    this.presentation = { ...this.presentation, ...next };
  }

  async loadSnapshot(requestedIntent: KataSnapshotIntent): Promise<boolean> {
    const intent = normalizeIntent(requestedIntent);
    const sequence = ++this.requestSequence;
    const previousSnapshot = this.state.snapshot;
    this.state = { phase: "loading", snapshot: previousSnapshot, intent, error: null };

    let response: KataWorkspaceSnapshotResponse;
    try {
      response = await this.load(intent);
    } catch (error) {
      if (sequence !== this.requestSequence) return false;
      this.state = {
        phase: "degraded",
        snapshot: previousSnapshot,
        intent,
        error: errorMessage(error),
      };
      throw error;
    }

    if (sequence !== this.requestSequence) return false;
    if (!responseIdentityMatches(response, intent)) {
      const error = new Error("Kata snapshot response does not match the current request intent");
      this.state = { phase: "degraded", snapshot: previousSnapshot, intent, error: error.message };
      throw error;
    }

    const key = orderingKey(response);
    const acceptedGeneration = this.acceptedGenerations[key];
    if (acceptedGeneration !== undefined && response.generation < acceptedGeneration) {
      this.restoreAcceptedState(previousSnapshot);
      return false;
    }

    this.acceptedGenerations[key] = response.generation;
    this.acceptedIntent = intent;
    this.state = { phase: "accepted", snapshot: response, intent, error: null };
    return true;
  }

  retry(): Promise<boolean> {
    if (!this.state.intent) return Promise.resolve(false);
    return this.loadSnapshot(this.state.intent);
  }

  private restoreAcceptedState(snapshot: KataWorkspaceSnapshotResponse | null): void {
    if (snapshot && this.acceptedIntent) {
      this.state = { phase: "accepted", snapshot, intent: this.acceptedIntent, error: null };
      return;
    }
    this.state = initialState;
  }
}

export function createKataAuthorityStore(options: CreateKataAuthorityStoreOptions = {}): KataAuthorityStore {
  return new KataAuthorityStore(options);
}
