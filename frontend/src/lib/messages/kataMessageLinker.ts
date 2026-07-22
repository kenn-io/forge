import type { KataTaskAPI, KataTaskMetadataPatch, KataTaskMutationTarget } from "../api/kata/taskTypes";
import type {
  KataSelectedIssueAuthority,
  KataSelectedIssueDetail,
} from "../features/kata/kataAuxiliaryAuthority.svelte";
import { acknowledgeKataMutationThenRevalidate } from "../features/kata/kataMutationRevalidation";
import type { MessageLinkInput } from "./messageLinks";
import { computeAddMessageLinkPatch, readMessageLinks } from "./messageLinks";

export interface MessageIssueLinker {
  linkMessage(issueUid: string, input: MessageLinkInput): Promise<{ qualified_id: string }>;
}

export function createMessageIssueLinker(
  authority: KataSelectedIssueAuthority,
  api: Pick<KataTaskAPI, "patchIssueMetadata">,
  actor = "middleman",
): MessageIssueLinker {
  const queues = new Map<string, Promise<void>>();

  async function linkMessage(issueUid: string, input: MessageLinkInput): Promise<{ qualified_id: string }> {
    let result: { qualified_id: string } | undefined;
    const previous = queues.get(issueUid) ?? Promise.resolve();
    const next = previous
      .catch(() => {})
      .then(async () => {
        const selected = await authority.selectIssue(issueUid);
        result = await patchFreshDetail(authority, api, actor, issueUid, selected.detail, selected.daemonID, input);
      });
    queues.set(issueUid, next);
    try {
      await next;
      if (!result) throw new Error("message link result unavailable");
      return result;
    } finally {
      if (queues.get(issueUid) === next) {
        queues.delete(issueUid);
      }
    }
  }

  return { linkMessage };
}

async function patchFreshDetail(
  authority: KataSelectedIssueAuthority,
  api: Pick<KataTaskAPI, "patchIssueMetadata">,
  actor: string,
  issueUID: string,
  fresh: KataSelectedIssueDetail,
  daemonID: string,
  input: MessageLinkInput,
): Promise<{ qualified_id: string }> {
  const patch = computeAddMessageLinkPatch(readMessageLinks(fresh.issue.metadata), input);
  if (patch === null) {
    return { qualified_id: fresh.issue.qualified_id };
  }
  const metadataPatch: KataTaskMetadataPatch = { mail_links: patch.mail_links };
  if (!fresh.etag) throw new Error(`Kata snapshot did not include an ETag for ${fresh.issue.qualified_id}`);
  const etag = fresh.etag;
  const revalidation = await acknowledgeKataMutationThenRevalidate(
    () => api.patchIssueMetadata(mutationTarget(fresh), actor, metadataPatch, etag, { daemonId: daemonID }),
    () => authority.selectIssue(issueUID).then(() => true),
  );
  const replacement = await revalidation.replacement;
  if (!replacement.replacementAccepted) {
    throw new Error(replacement.replacementError ?? "Kata snapshot replacement was not accepted.");
  }
  return { qualified_id: fresh.issue.qualified_id };
}

function mutationTarget(detail: KataSelectedIssueDetail): KataTaskMutationTarget {
  return {
    project_id: detail.issue.project_id,
    ref: detail.issue.uid,
  };
}
