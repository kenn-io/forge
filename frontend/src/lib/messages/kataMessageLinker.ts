import type { KataTaskAPI, KataTaskDetail, KataTaskMetadataPatch, KataTaskMutationTarget } from "../api/kata/taskTypes";
import type { MessageLinkInput } from "./messageLinks";
import { computeAddMessageLinkPatch, readMessageLinks } from "./messageLinks";

export interface MessageIssueLinker {
  linkMessage(issueUid: string, input: MessageLinkInput): Promise<{ qualified_id: string }>;
}

export function createMessageIssueLinker(
  api: Pick<KataTaskAPI, "issue" | "patchIssueMetadata">,
  actor = "middleman",
): MessageIssueLinker {
  const queues = new Map<string, Promise<void>>();

  async function linkMessage(issueUid: string, input: MessageLinkInput): Promise<{ qualified_id: string }> {
    let result: { qualified_id: string } | undefined;
    const previous = queues.get(issueUid) ?? Promise.resolve();
    const next = previous
      .catch(() => {})
      .then(async () => {
        const fresh = await api.issue(issueUid);
        result = await patchFreshDetail(api, actor, fresh, input);
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
  api: Pick<KataTaskAPI, "patchIssueMetadata">,
  actor: string,
  fresh: KataTaskDetail,
  input: MessageLinkInput,
): Promise<{ qualified_id: string }> {
  const patch = computeAddMessageLinkPatch(readMessageLinks(fresh.issue.metadata), input);
  if (patch === null) {
    return { qualified_id: fresh.issue.qualified_id };
  }
  const metadataPatch: KataTaskMetadataPatch = { mail_links: patch.mail_links };
  const response = await api.patchIssueMetadata(
    mutationTarget(fresh),
    actor,
    metadataPatch,
    fresh.etag ?? `"rev-${fresh.issue.revision}"`,
  );
  return { qualified_id: response.issue?.qualified_id ?? fresh.issue.qualified_id };
}

function mutationTarget(detail: KataTaskDetail): KataTaskMutationTarget {
  return {
    project_id: detail.issue.project_id,
    ref: detail.issue.uid,
  };
}
