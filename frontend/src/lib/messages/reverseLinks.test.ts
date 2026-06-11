import { describe, expect, test } from "vite-plus/test";
import type { IssueSummary, MessageLinkRef } from "./types";
import { buildLinkedMessagesIndex, findIssuesLinkedToMessage } from "./reverseLinks";

function makeLink(overrides: Partial<MessageLinkRef> = {}): MessageLinkRef {
  return {
    message_id: 1001,
    subject: "Project sync",
    from: "alice@example.com",
    sent_at: "2026-05-15T09:00:00Z",
    added_at: "2026-05-19T10:00:00Z",
    ...overrides,
  };
}

function makeIssue(overrides: Partial<IssueSummary> & { mail_links?: MessageLinkRef[] } = {}): IssueSummary {
  const { mail_links, ...rest } = overrides;
  return {
    uid: "uid-1",
    short_id: "ISS-1",
    qualified_id: "Inbox#ISS-1",
    title: "Test issue",
    status: "open",
    metadata: mail_links !== undefined ? { mail_links } : {},
    ...rest,
  };
}

describe("findIssuesLinkedToMessage", () => {
  test("returns empty array when no issues link the message", () => {
    const issues = [makeIssue({ uid: "uid-1", mail_links: [makeLink({ message_id: 2000 })] })];
    expect(findIssuesLinkedToMessage(issues, 1001)).toEqual([]);
  });

  test("returns empty array for an empty issues list", () => {
    expect(findIssuesLinkedToMessage([], 1001)).toEqual([]);
  });

  test("returns one ref when exactly one issue links the message", () => {
    const issues = [
      makeIssue({
        uid: "uid-1",
        short_id: "ISS-1",
        qualified_id: "Inbox#ISS-1",
        mail_links: [makeLink({ message_id: 1001 })],
      }),
    ];
    const result = findIssuesLinkedToMessage(issues, 1001);
    expect(result).toHaveLength(1);
    expect(result[0]).toEqual({
      uid: "uid-1",
      short_id: "ISS-1",
      qualified_id: "Inbox#ISS-1",
      title: "Test issue",
      status: "open",
    });
  });

  test("returns refs for all issues that link the message", () => {
    const issues = [
      makeIssue({
        uid: "uid-1",
        short_id: "ISS-1",
        qualified_id: "Inbox#ISS-1",
        title: "First",
        mail_links: [makeLink({ message_id: 1001 })],
      }),
      makeIssue({
        uid: "uid-2",
        short_id: "ISS-2",
        qualified_id: "Inbox#ISS-2",
        title: "Second",
        mail_links: [makeLink({ message_id: 2000 })],
      }),
      makeIssue({
        uid: "uid-3",
        short_id: "ISS-3",
        qualified_id: "Inbox#ISS-3",
        title: "Third",
        mail_links: [makeLink({ message_id: 1001 })],
      }),
    ];
    const result = findIssuesLinkedToMessage(issues, 1001);
    expect(result).toHaveLength(2);
    expect(result.map((ref) => ref.uid)).toEqual(["uid-1", "uid-3"]);
  });

  test("issue with no mail_links metadata is skipped silently", () => {
    const issues = [makeIssue({ uid: "uid-1", metadata: {} })];
    expect(findIssuesLinkedToMessage(issues, 1001)).toEqual([]);
  });
});

describe("buildLinkedMessagesIndex", () => {
  test("empty issues array returns empty result", () => {
    expect(buildLinkedMessagesIndex([])).toEqual([]);
  });

  test("issues with no message links return empty result", () => {
    const issues = [makeIssue({ uid: "uid-1" })];
    expect(buildLinkedMessagesIndex(issues)).toEqual([]);
  });

  test("single issue with two links produces two rows", () => {
    const link1 = makeLink({ message_id: 1001, added_at: "2026-05-19T10:00:00Z" });
    const link2 = makeLink({
      message_id: 2001,
      subject: "Another thread",
      from: "bob@example.com",
      added_at: "2026-05-18T08:00:00Z",
    });
    const issues = [
      makeIssue({ uid: "uid-1", short_id: "ISS-1", qualified_id: "Inbox#ISS-1", mail_links: [link1, link2] }),
    ];
    const rows = buildLinkedMessagesIndex(issues);
    expect(rows).toHaveLength(2);
    const ids = rows.map((row) => row.message.message_id);
    expect(ids).toContain(1001);
    expect(ids).toContain(2001);
    for (const row of rows) {
      expect(row.issues).toHaveLength(1);
      expect(row.issues[0]!.uid).toBe("uid-1");
    }
  });

  test("two issues linking the same message produce one row with two issue refs", () => {
    const link = makeLink({ message_id: 1001 });
    const issues = [
      makeIssue({ uid: "uid-1", short_id: "ISS-1", qualified_id: "Inbox#ISS-1", title: "Alpha", mail_links: [link] }),
      makeIssue({ uid: "uid-2", short_id: "ISS-2", qualified_id: "Inbox#ISS-2", title: "Beta", mail_links: [link] }),
    ];
    const rows = buildLinkedMessagesIndex(issues);
    expect(rows).toHaveLength(1);
    expect(rows[0]!.message.message_id).toBe(1001);
    expect(rows[0]!.issues).toHaveLength(2);
    const uids = rows[0]!.issues.map((ref) => ref.uid);
    expect(uids).toContain("uid-1");
    expect(uids).toContain("uid-2");
  });

  test("most_recent_added_at on a shared-message row is max added_at across linking issues", () => {
    const earlier = "2026-05-17T10:00:00Z";
    const later = "2026-05-19T15:00:00Z";
    const issues = [
      makeIssue({
        uid: "uid-1",
        short_id: "ISS-1",
        qualified_id: "Inbox#ISS-1",
        mail_links: [makeLink({ message_id: 1001, added_at: earlier })],
      }),
      makeIssue({
        uid: "uid-2",
        short_id: "ISS-2",
        qualified_id: "Inbox#ISS-2",
        mail_links: [makeLink({ message_id: 1001, added_at: later })],
      }),
    ];
    const rows = buildLinkedMessagesIndex(issues);
    expect(rows[0]!.most_recent_added_at).toBe(later);
  });

  test("rows are sorted by most_recent_added_at descending", () => {
    const issues = [
      makeIssue({
        uid: "uid-1",
        short_id: "ISS-1",
        qualified_id: "Inbox#ISS-1",
        mail_links: [makeLink({ message_id: 1001, added_at: "2026-05-17T00:00:00Z" })],
      }),
      makeIssue({
        uid: "uid-2",
        short_id: "ISS-2",
        qualified_id: "Inbox#ISS-2",
        mail_links: [makeLink({ message_id: 2001, added_at: "2026-05-19T00:00:00Z" })],
      }),
      makeIssue({
        uid: "uid-3",
        short_id: "ISS-3",
        qualified_id: "Inbox#ISS-3",
        mail_links: [makeLink({ message_id: 3001, added_at: "2026-05-18T00:00:00Z" })],
      }),
    ];
    const rows = buildLinkedMessagesIndex(issues);
    expect(rows).toHaveLength(3);
    expect(rows.map((row) => row.message.message_id)).toEqual([2001, 3001, 1001]);
  });

  test("duplicate mail_links within a single issue contribute only one issue ref", () => {
    const issues = [
      makeIssue({
        uid: "uid-1",
        short_id: "ISS-1",
        qualified_id: "Inbox#ISS-1",
        mail_links: [
          makeLink({ message_id: 1001, added_at: "2026-05-17T00:00:00Z" }),
          makeLink({ message_id: 1001, added_at: "2026-05-19T00:00:00Z" }),
        ],
      }),
    ];
    const rows = buildLinkedMessagesIndex(issues);
    expect(rows).toHaveLength(1);
    expect(rows[0]!.issues).toHaveLength(1);
    expect(rows[0]!.issues[0]!.uid).toBe("uid-1");
  });

  test("duplicate links for one message use the latest added_at", () => {
    const issues = [
      makeIssue({
        uid: "uid-A",
        short_id: "ISS-A",
        qualified_id: "Inbox#ISS-A",
        mail_links: [
          makeLink({ message_id: 1001, added_at: "2026-05-17T00:00:00Z" }),
          makeLink({ message_id: 1001, added_at: "2026-05-30T00:00:00Z" }),
        ],
      }),
      makeIssue({
        uid: "uid-B",
        short_id: "ISS-B",
        qualified_id: "Inbox#ISS-B",
        mail_links: [makeLink({ message_id: 2001, added_at: "2026-05-25T00:00:00Z" })],
      }),
    ];
    const rows = buildLinkedMessagesIndex(issues);
    expect(rows.map((row) => row.message.message_id)).toEqual([1001, 2001]);
    expect(rows[0]!.most_recent_added_at).toBe("2026-05-30T00:00:00Z");
  });

  test("malformed mail_links metadata is silently skipped", () => {
    const issues = [
      makeIssue({ uid: "uid-1", metadata: { mail_links: "not-an-array" } }),
      makeIssue({
        uid: "uid-2",
        metadata: { mail_links: [{ message_id: 0, subject: "bad" }] },
      }),
      makeIssue({
        uid: "uid-3",
        short_id: "ISS-3",
        qualified_id: "Inbox#ISS-3",
        mail_links: [makeLink({ message_id: 1001 })],
      }),
    ];
    const rows = buildLinkedMessagesIndex(issues);
    expect(rows).toHaveLength(1);
    expect(rows[0]!.issues[0]!.uid).toBe("uid-3");
  });
});
