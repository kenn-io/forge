import { describe, expect, it, vi } from "vite-plus/test";

import type {
  KataTaskAPI,
  KataTaskDetail,
  KataTaskMetadataPatch,
  KataTaskMutationResponse,
  KataTaskMutationTarget,
  KataTaskSummary,
} from "../api/kata/taskTypes";
import type { MessageLinkInput } from "./messageLinks";
import { createMessageIssueLinker } from "./kataMessageLinker";

const fetchedAt = "2026-05-18T10:00:00Z";

function issue(metadata: Record<string, unknown> = {}, revision = 1): KataTaskSummary {
  return {
    id: 42,
    uid: "issue-pay-rent",
    project_id: 7,
    short_id: "rent",
    qualified_id: "Kata#rent",
    title: "Pay rent",
    status: "open",
    project_uid: "project-kata",
    project_name: "Kata",
    metadata,
    revision,
    author: "tester",
    created_at: fetchedAt,
    updated_at: fetchedAt,
  };
}

function detail(row: KataTaskSummary, etag = `"rev-${row.revision}"`): KataTaskDetail {
  return {
    issue: { ...row, body: "body" },
    comments: [],
    labels: [],
    links: [],
    children: [],
    etag,
  };
}

function input(messageID: number, subject = `Message ${messageID}`): MessageLinkInput {
  return {
    message_id: messageID,
    conversation_id: messageID,
    subject,
    from: "alice@example.com",
    sent_at: "2026-05-15T09:00:00Z",
  };
}

function makeAPI() {
  let current = issue();
  const issueMock = vi.fn(async () => detail({ ...current, metadata: { ...current.metadata } }));
  const patchIssueMetadata = vi.fn(
    async (
      _target: KataTaskMutationTarget,
      _actor: string,
      patch: KataTaskMetadataPatch,
      _ifMatch: string,
    ): Promise<KataTaskMutationResponse> => {
      current = {
        ...current,
        metadata: { ...current.metadata, ...patch },
        revision: current.revision + 1,
      };
      return { changed: true, issue: current, etag: `"rev-${current.revision}"` };
    },
  );
  const api = {
    issue: issueMock,
    patchIssueMetadata,
  } as Pick<KataTaskAPI, "issue" | "patchIssueMetadata">;
  return {
    api,
    issue: issueMock,
    patchIssueMetadata,
    links: () => (Array.isArray(current.metadata.mail_links) ? current.metadata.mail_links : []),
  };
}

describe("createMessageIssueLinker", () => {
  it("serializes same-issue links and computes the second patch from the post-prior-write state", async () => {
    const { api, patchIssueMetadata, links } = makeAPI();
    const linker = createMessageIssueLinker(api);

    await Promise.all([
      linker.linkMessage("issue-pay-rent", input(5001, "Alpha")),
      linker.linkMessage("issue-pay-rent", input(5002, "Bravo")),
    ]);

    expect(patchIssueMetadata).toHaveBeenCalledTimes(2);
    expect(patchIssueMetadata.mock.calls.map((call) => call[3])).toEqual(['"rev-1"', '"rev-2"']);
    expect(
      links()
        .map((link) => (link as { message_id: number }).message_id)
        .sort((a, b) => a - b),
    ).toEqual([5001, 5002]);
  });

  it("skips the metadata patch when the message is already linked", async () => {
    const { api, patchIssueMetadata } = makeAPI();
    const linker = createMessageIssueLinker(api);

    await linker.linkMessage("issue-pay-rent", input(5001, "Alpha"));
    const result = await linker.linkMessage("issue-pay-rent", input(5001, "Alpha"));

    expect(result).toEqual({ qualified_id: "Kata#rent" });
    expect(patchIssueMetadata).toHaveBeenCalledTimes(1);
  });
});
