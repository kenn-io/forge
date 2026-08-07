import { Effect } from "effect";
import type { KataWorkspaceSnapshotProjection } from "../../api/kata/snapshotProjection.js";
import type {
  KataTaskCreateDraft,
  KataTaskEditPatch,
  KataCreateRecurrenceInput,
  KataPatchRecurrenceInput,
  KataRecurrence,
  KataRecurrencesResponse,
  KataTaskSummary,
} from "../../api/kata/taskTypes.js";
import type { KataMutationIdentity, KataMutationResolution } from "./kata-workflow.js";

export interface KataMutationEvidence {
  readonly family: string;
  readonly operation: string;
  readonly target: string;
  readonly selectedIssueUID?: string | undefined;
  readonly identity: (daemonId: string) => KataMutationIdentity;
  readonly isApplied: (snapshot: KataWorkspaceSnapshotProjection) => boolean;
}

type SnapshotDetail = NonNullable<KataWorkspaceSnapshotProjection["selected_detail"]>;
type SnapshotIssue = KataWorkspaceSnapshotProjection["issues"][number];
type SnapshotLink = SnapshotDetail["links"][number];

function makeEvidence(
  family: string,
  operation: string,
  target: string,
  isApplied: (snapshot: KataWorkspaceSnapshotProjection) => boolean,
  selectedIssueUID?: string,
): KataMutationEvidence {
  return {
    family,
    operation,
    target,
    ...(selectedIssueUID === undefined ? {} : { selectedIssueUID }),
    identity: (daemonId) => kataMutationIdentity(daemonId, family, operation, target),
    isApplied,
  };
}

export function kataMutationIdentity(
  daemonId: string,
  family: string,
  operation: string,
  target: string,
): KataMutationIdentity {
  return {
    key: JSON.stringify([daemonId, family, target]),
    daemonId,
    operation,
    target,
  };
}

function selectedDetail(snapshot: KataWorkspaceSnapshotProjection, uid: string) {
  const detail = snapshot.selected_detail;
  return detail?.issue.uid === uid ? detail : undefined;
}

function jsonEqual(left: unknown, right: unknown): boolean {
  return JSON.stringify(left) === JSON.stringify(right);
}

function metadataMatches(
  metadata: Readonly<Record<string, unknown>>,
  patch: Readonly<Record<string, unknown>>,
): boolean {
  return Object.entries(patch).every(([key, value]) =>
    value === null ? !Object.hasOwn(metadata, key) : jsonEqual(metadata[key], value),
  );
}

function peerMatches(peer: { readonly uid: string; readonly short_id: string }, ref: string): boolean {
  return peer.uid === ref || peer.short_id === ref;
}

function otherPeer(link: SnapshotLink, uid: string) {
  if (link.from.uid === uid) return link.to;
  if (link.to.uid === uid) return link.from;
  return undefined;
}

function hasRelatedLink(detail: SnapshotDetail, uid: string, ref: string): boolean {
  return detail.links.some((link) => {
    const peer = link.type === "related" ? otherPeer(link, uid) : undefined;
    return peer !== undefined && peerMatches(peer, ref);
  });
}

function hasBlocksLink(detail: SnapshotDetail, uid: string, ref: string): boolean {
  return detail.links.some((link) => link.type === "blocks" && link.from.uid === uid && peerMatches(link.to, ref));
}

function hasBlockedByLink(detail: SnapshotDetail, uid: string, ref: string): boolean {
  return detail.links.some((link) => link.type === "blocks" && link.to.uid === uid && peerMatches(link.from, ref));
}

function editLinksMatch(detail: SnapshotDetail, uid: string, patch: KataTaskEditPatch): boolean {
  const delta = patch.links_delta;
  if (delta === undefined) return true;
  if ((delta.add_related ?? []).some((ref) => !hasRelatedLink(detail, uid, ref))) return false;
  if ((delta.add_blocks ?? []).some((ref) => !hasBlocksLink(detail, uid, ref))) return false;
  if ((delta.add_blocked_by ?? []).some((ref) => !hasBlockedByLink(detail, uid, ref))) return false;
  if (delta.set_parent !== undefined) {
    if (delta.set_parent === null && detail.parent !== undefined) return false;
    if (typeof delta.set_parent === "string" && !detail.parent) return false;
    if (typeof delta.set_parent === "string" && detail.parent && !peerMatches(detail.parent, delta.set_parent)) {
      return false;
    }
  }
  return !(delta.remove ?? []).some((ref) =>
    detail.links.some((link) => {
      const peer = otherPeer(link, uid);
      return peer !== undefined && peerMatches(peer, ref);
    }),
  );
}

function issueMatchesDraft(issue: SnapshotIssue, projectUID: string, draft: KataTaskCreateDraft): boolean {
  if (issue.project_uid !== projectUID || issue.title !== draft.title) return false;
  if (draft.body !== undefined && issue.body !== draft.body) return false;
  if (draft.owner !== undefined && issue.owner !== draft.owner) return false;
  if (draft.priority !== undefined && issue.priority !== draft.priority) return false;
  if (draft.labels !== undefined) {
    const actual = new Set(issue.labels ?? []);
    if (actual.size !== new Set(draft.labels).size || draft.labels.some((label) => !actual.has(label))) return false;
  }
  return draft.metadata === undefined || metadataMatches(issue.metadata, draft.metadata);
}

export function commentMutationEvidence(
  uid: string,
  author: string,
  body: string,
  baselineMatches: number,
): KataMutationEvidence {
  return makeEvidence(
    "issue-comment",
    "add Kata comment",
    uid,
    (snapshot) => {
      const detail = selectedDetail(snapshot, uid);
      return (
        (detail?.comments.filter((comment) => comment.author === author && comment.body === body).length ?? 0) >
        baselineMatches
      );
    },
    uid,
  );
}

export function editMutationEvidence(uid: string, patch: KataTaskEditPatch): KataMutationEvidence {
  return makeEvidence(
    "issue-edit",
    "edit Kata task",
    uid,
    (snapshot) => {
      const detail = selectedDetail(snapshot, uid);
      if (!detail) return false;
      const hasEvidence = patch.title !== undefined || patch.body !== undefined || patch.links_delta !== undefined;
      return (
        hasEvidence &&
        (patch.title === undefined || detail.issue.title === patch.title) &&
        (patch.body === undefined || detail.issue.body === patch.body) &&
        editLinksMatch(detail, uid, patch)
      );
    },
    uid,
  );
}

export function ownerMutationEvidence(uid: string, owner: string | undefined): KataMutationEvidence {
  return makeEvidence(
    "issue-owner",
    owner === undefined ? "unassign Kata task" : "assign Kata task",
    uid,
    (snapshot) => {
      const issue = selectedDetail(snapshot, uid)?.issue;
      return issue !== undefined && issue.owner === owner;
    },
    uid,
  );
}

export function priorityMutationEvidence(uid: string, priority: number | null): KataMutationEvidence {
  return makeEvidence(
    "issue-priority",
    "set Kata priority",
    uid,
    (snapshot) => {
      const issue = selectedDetail(snapshot, uid)?.issue;
      return issue !== undefined && issue.priority === (priority ?? undefined);
    },
    uid,
  );
}

export function labelMutationEvidence(uid: string, label: string, present: boolean): KataMutationEvidence {
  return makeEvidence(
    "issue-label",
    present ? "add Kata label" : "remove Kata label",
    `${uid}:${label}`,
    (snapshot) => {
      const detail = selectedDetail(snapshot, uid);
      const found = detail?.labels.some((candidate) => candidate.label === label) ?? false;
      return found === present;
    },
    uid,
  );
}

export function metadataMutationEvidence(uid: string, patch: Readonly<Record<string, unknown>>): KataMutationEvidence {
  return makeEvidence(
    "issue-metadata",
    "update Kata metadata",
    uid,
    (snapshot) => {
      const issue = selectedDetail(snapshot, uid)?.issue;
      return issue !== undefined && Object.keys(patch).length > 0 && metadataMatches(issue.metadata, patch);
    },
    uid,
  );
}

export function moveMutationEvidence(uid: string, projectUID: string): KataMutationEvidence {
  return makeEvidence(
    "issue-project",
    "move Kata task",
    uid,
    (snapshot) => {
      const detailIssue = selectedDetail(snapshot, uid)?.issue;
      const issue = detailIssue ?? snapshot.issues.find((candidate) => candidate.uid === uid);
      return issue?.project_uid === projectUID;
    },
    uid,
  );
}

export function statusMutationEvidence(
  uid: string,
  status: "open" | "closed",
  reason?: KataTaskSummary["closed_reason"],
): KataMutationEvidence {
  return makeEvidence(
    "issue-status",
    status === "open" ? "reopen Kata task" : "close Kata task",
    uid,
    (snapshot) => {
      const detailIssue = selectedDetail(snapshot, uid)?.issue;
      const issue = detailIssue ?? snapshot.issues.find((candidate) => candidate.uid === uid);
      return issue?.status === status && (status === "open" || reason === undefined || issue.closed_reason === reason);
    },
    uid,
  );
}

export function projectCreateMutationEvidence(name: string, baselineUIDs: ReadonlySet<string>): KataMutationEvidence {
  const normalizedName = name.trim();
  return makeEvidence("project-create", "create Kata project", normalizedName, (snapshot) => {
    const matching = snapshot.projects.filter(
      (project) => !baselineUIDs.has(project.uid) && project.name === normalizedName,
    );
    return matching.length === 1;
  });
}

export function issueCreateMutationEvidence(
  projectUID: string,
  draft: KataTaskCreateDraft,
  baselineUIDs: ReadonlySet<string>,
): KataMutationEvidence {
  return makeEvidence("issue-create", "create Kata task", `${projectUID}:${draft.title}`, (snapshot) => {
    const matching = snapshot.issues.filter(
      (issue) => !baselineUIDs.has(issue.uid) && issueMatchesDraft(issue, projectUID, draft),
    );
    return matching.length === 1;
  });
}

function stringArrayEqual(left: readonly string[], right: readonly string[]): boolean {
  const leftSet = new Set(left);
  const rightSet = new Set(right);
  return leftSet.size === rightSet.size && left.every((value) => rightSet.has(value));
}

export function recurrenceCreateMatches(recurrence: KataRecurrence, input: KataCreateRecurrenceInput): boolean {
  return (
    recurrence.rrule === input.rrule &&
    recurrence.dtstart === input.dtstart &&
    recurrence.timezone === input.timezone &&
    recurrence.template_title === input.template.title &&
    recurrence.template_body === (input.template.body ?? "") &&
    recurrence.template_owner === input.template.owner &&
    recurrence.template_priority === input.template.priority &&
    stringArrayEqual(recurrence.template_labels, input.template.labels ?? []) &&
    metadataMatches(recurrence.template_metadata, input.template.metadata ?? {})
  );
}

export function recurrencePatchMatches(recurrence: KataRecurrence, input: KataPatchRecurrenceInput): boolean {
  const template = input.template;
  return (
    (input.rrule === undefined || recurrence.rrule === input.rrule) &&
    (input.dtstart === undefined || recurrence.dtstart === input.dtstart) &&
    (input.timezone === undefined || recurrence.timezone === input.timezone) &&
    (template?.title === undefined || recurrence.template_title === template.title) &&
    (template?.body === undefined || recurrence.template_body === template.body) &&
    (template?.owner === undefined || recurrence.template_owner === (template.owner ?? undefined)) &&
    (template?.priority === undefined || recurrence.template_priority === (template.priority ?? undefined)) &&
    (template?.labels === undefined || stringArrayEqual(recurrence.template_labels, template.labels)) &&
    (template?.metadata === undefined || metadataMatches(recurrence.template_metadata, template.metadata))
  );
}

export function reconcileRecurrenceMutation<E, R>(
  baseline: readonly KataRecurrence[],
  readFresh: Effect.Effect<KataRecurrencesResponse, E, R>,
  isApplied: (recurrences: readonly KataRecurrence[]) => boolean,
): Effect.Effect<KataMutationResolution, E, R> {
  return Effect.map(readFresh, (fresh) => {
    if (isApplied(fresh.recurrences)) return "applied";
    return jsonEqual(fresh.recurrences, baseline) ? "absent" : "ambiguous";
  });
}
