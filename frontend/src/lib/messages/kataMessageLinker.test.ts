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
  const selectIssue = vi.fn(async () => ({
    daemonID: "daemon-home",
    detail: detail({ ...current, metadata: { ...current.metadata } }),
  }));
  const patchIssueMetadata = vi.fn(
    async (
      _target: KataTaskMutationTarget,
      _actor: string,
      patch: KataTaskMetadataPatch,
      _ifMatch: string,
      _options: { daemonId: string },
    ): Promise<KataTaskMutationResponse> => {
      current = {
        ...current,
        metadata: { ...current.metadata, ...patch },
        revision: current.revision + 1,
      };
      return { changed: true };
    },
  );
  const api = {
    patchIssueMetadata,
  } as Pick<KataTaskAPI, "patchIssueMetadata">;
  return {
    authority: { selectIssue },
    api,
    selectIssue,
    patchIssueMetadata,
    links: () => (Array.isArray(current.metadata.mail_links) ? current.metadata.mail_links : []),
  };
}

describe("createMessageIssueLinker", () => {
  it("uses selected snapshot metadata and ETag before linking", async () => {
    const fresh = detail(
      issue({
        mail_links: [
          {
            message_id: 4000,
            subject: "Existing",
            from: "bob@example.com",
            sent_at: fetchedAt,
            added_at: fetchedAt,
          },
        ],
      }),
      '"rev-7"',
    );
    const authority = { selectIssue: vi.fn(async () => ({ daemonID: "daemon-a", detail: fresh })) };
    const patchIssueMetadata = vi.fn(async (): Promise<KataTaskMutationResponse> => ({ changed: true }));
    const linker = createMessageIssueLinker(authority, { patchIssueMetadata });

    await expect(linker.linkMessage("issue-pay-rent", input(5001, "Alpha"))).resolves.toEqual({
      qualified_id: "Kata#rent",
    });

    expect(authority.selectIssue).toHaveBeenCalledWith("issue-pay-rent");
    expect(patchIssueMetadata).toHaveBeenCalledWith(
      { project_id: 7, ref: "issue-pay-rent" },
      "middleman",
      {
        mail_links: expect.arrayContaining([
          expect.objectContaining({ message_id: 4000 }),
          expect.objectContaining({ message_id: 5001 }),
        ]),
      },
      '"rev-7"',
      { daemonId: "daemon-a" },
    );
  });

  it("serializes same-issue links and computes the second patch from the post-prior-write state", async () => {
    const { authority, api, selectIssue, patchIssueMetadata, links } = makeAPI();
    const linker = createMessageIssueLinker(authority, api);

    await Promise.all([
      linker.linkMessage("issue-pay-rent", input(5001, "Alpha")),
      linker.linkMessage("issue-pay-rent", input(5002, "Bravo")),
    ]);

    expect(patchIssueMetadata).toHaveBeenCalledTimes(2);
    expect(selectIssue).toHaveBeenCalledTimes(2);
    expect(patchIssueMetadata.mock.calls.map((call) => call[3])).toEqual(['"rev-1"', '"rev-2"']);
    expect(patchIssueMetadata.mock.calls.map((call) => call[4])).toEqual([
      { daemonId: "daemon-home" },
      { daemonId: "daemon-home" },
    ]);
    expect(
      links()
        .map((link) => (link as { message_id: number }).message_id)
        .sort((a, b) => a - b),
    ).toEqual([5001, 5002]);
  });

  it("skips the metadata patch when the message is already linked", async () => {
    const { authority, api, patchIssueMetadata } = makeAPI();
    const linker = createMessageIssueLinker(authority, api);

    await linker.linkMessage("issue-pay-rent", input(5001, "Alpha"));
    const result = await linker.linkMessage("issue-pay-rent", input(5001, "Alpha"));

    expect(result).toEqual({ qualified_id: "Kata#rent" });
    expect(patchIssueMetadata).toHaveBeenCalledTimes(1);
  });

  it("pins the patch to the selected snapshot daemon when active daemon state changes", async () => {
    let activeDaemon = "daemon-a";
    const authority = {
      selectIssue: vi.fn(async () => {
        const selection = { daemonID: activeDaemon, detail: detail(issue(), '"rev-7"') };
        activeDaemon = "daemon-b";
        return selection;
      }),
    };
    const patchIssueMetadata = vi.fn(async (): Promise<KataTaskMutationResponse> => ({ changed: true }));
    const linker = createMessageIssueLinker(authority, { patchIssueMetadata });

    await linker.linkMessage("issue-pay-rent", input(5001));

    expect(activeDaemon).toBe("daemon-b");
    expect(patchIssueMetadata).toHaveBeenCalledWith(expect.any(Object), "middleman", expect.any(Object), '"rev-7"', {
      daemonId: "daemon-a",
    });
  });
});
