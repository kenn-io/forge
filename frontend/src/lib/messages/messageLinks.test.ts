import { afterEach, beforeEach, describe, expect, test, vi } from "vite-plus/test";
import type { MessageLinkRef } from "./types";
import {
  computeAddMessageLinkPatch,
  computeRemoveMessageLinkPatch,
  readMessageLinks,
  type MessageLinkInput,
} from "./messageLinks";

const BASE_INPUT: MessageLinkInput = {
  message_id: 1001,
  conversation_id: 1001,
  subject: "Project sync",
  from: "alice@example.com",
  sent_at: "2026-05-15T09:00:00Z",
};

const FIXED_NOW = "2026-05-19T12:00:00.000Z";

function existing(overrides: Partial<MessageLinkRef> = {}): MessageLinkRef {
  return {
    message_id: 2002,
    conversation_id: 2002,
    subject: "Earlier thread",
    from: "bob@example.com",
    sent_at: "2026-05-10T08:00:00Z",
    added_at: "2026-05-12T15:00:00Z",
    ...overrides,
  };
}

describe("computeAddMessageLinkPatch", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date(FIXED_NOW));
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  test("empty array yields a patch with a single entry and added_at set", () => {
    const patch = computeAddMessageLinkPatch([], BASE_INPUT);
    expect(patch).not.toBeNull();
    expect(patch).toEqual({
      mail_links: [
        {
          message_id: 1001,
          conversation_id: 1001,
          subject: "Project sync",
          from: "alice@example.com",
          sent_at: "2026-05-15T09:00:00Z",
          added_at: FIXED_NOW,
        },
      ],
    });
  });

  test("omits conversation_id when not provided", () => {
    const { conversation_id: _omit, ...withoutConversation } = BASE_INPUT;
    void _omit;
    const patch = computeAddMessageLinkPatch([], withoutConversation);
    const links = (patch as { mail_links: MessageLinkRef[] }).mail_links;
    expect(links).toHaveLength(1);
    expect(links[0]).not.toHaveProperty("conversation_id");
    expect(links[0]!.message_id).toBe(1001);
  });

  test("appends new entry to end and preserves existing links", () => {
    const current = [existing()];
    const patch = computeAddMessageLinkPatch(current, BASE_INPUT);
    const links = (patch as { mail_links: MessageLinkRef[] }).mail_links;
    expect(links).toHaveLength(2);
    expect(links[0]).toEqual(current[0]);
    expect(links[1]!.message_id).toBe(1001);
  });

  test("re-adding the same message_id returns null", () => {
    const current = [
      existing({
        message_id: 1001,
        conversation_id: 1001,
        subject: "Project sync",
        from: "alice@example.com",
        sent_at: "2026-05-15T09:00:00Z",
        added_at: "2026-05-16T10:00:00Z",
      }),
    ];
    expect(computeAddMessageLinkPatch(current, BASE_INPUT)).toBeNull();
  });

  describe("validation", () => {
    test("rejects zero message_id", () => {
      expect(() => computeAddMessageLinkPatch([], { ...BASE_INPUT, message_id: 0 })).toThrow(
        /message_id must be a positive integer/,
      );
    });

    test("rejects negative message_id", () => {
      expect(() => computeAddMessageLinkPatch([], { ...BASE_INPUT, message_id: -1 })).toThrow(
        /message_id must be a positive integer/,
      );
    });

    test("rejects non-integer message_id", () => {
      expect(() => computeAddMessageLinkPatch([], { ...BASE_INPUT, message_id: 1.5 })).toThrow(
        /message_id must be a positive integer/,
      );
    });

    test("rejects zero conversation_id when provided", () => {
      expect(() => computeAddMessageLinkPatch([], { ...BASE_INPUT, conversation_id: 0 })).toThrow(
        /conversation_id must be a positive integer/,
      );
    });

    test("rejects negative conversation_id when provided", () => {
      expect(() => computeAddMessageLinkPatch([], { ...BASE_INPUT, conversation_id: -3 })).toThrow(
        /conversation_id must be a positive integer/,
      );
    });

    test("rejects unparseable sent_at", () => {
      expect(() => computeAddMessageLinkPatch([], { ...BASE_INPUT, sent_at: "not-a-date" })).toThrow(
        /sent_at is not a parseable datetime/,
      );
    });

    test("accepts a date-only sent_at", () => {
      const patch = computeAddMessageLinkPatch([], {
        ...BASE_INPUT,
        sent_at: "2026-05-15",
      });
      expect(patch).not.toBeNull();
    });

    test("accepts empty subject", () => {
      const patch = computeAddMessageLinkPatch([], { ...BASE_INPUT, subject: "" });
      const links = (patch as { mail_links: MessageLinkRef[] }).mail_links;
      expect(links[0]!.subject).toBe("");
    });

    test("accepts whitespace-only subject", () => {
      const patch = computeAddMessageLinkPatch([], { ...BASE_INPUT, subject: "   " });
      const links = (patch as { mail_links: MessageLinkRef[] }).mail_links;
      expect(links[0]!.subject).toBe("   ");
    });

    test("rejects empty from", () => {
      expect(() => computeAddMessageLinkPatch([], { ...BASE_INPUT, from: "" })).toThrow(/from is required/);
    });

    test("rejects whitespace-only from", () => {
      expect(() => computeAddMessageLinkPatch([], { ...BASE_INPUT, from: "\t \n " })).toThrow(/from is required/);
    });
  });

  test("truncates subject of 600 chars to length 500", () => {
    const longSubject = "a".repeat(600);
    const patch = computeAddMessageLinkPatch([], {
      ...BASE_INPUT,
      subject: longSubject,
    });
    const links = (patch as { mail_links: MessageLinkRef[] }).mail_links;
    const stored = links[0]!.subject;
    expect(stored).toHaveLength(500);
    expect(stored.endsWith("\u2026")).toBe(true);
    expect(stored.slice(0, 499)).toBe("a".repeat(499));
  });

  test("does not truncate subject at exactly the limit", () => {
    const exact = "b".repeat(500);
    const patch = computeAddMessageLinkPatch([], {
      ...BASE_INPUT,
      subject: exact,
    });
    const links = (patch as { mail_links: MessageLinkRef[] }).mail_links;
    expect(links[0]!.subject).toBe(exact);
  });

  test("slices a 400-char from down to 320 chars", () => {
    const longFrom = "x".repeat(400);
    const patch = computeAddMessageLinkPatch([], {
      ...BASE_INPUT,
      from: longFrom,
    });
    const links = (patch as { mail_links: MessageLinkRef[] }).mail_links;
    const stored = links[0]!.from;
    expect(stored).toHaveLength(320);
    expect(stored).toBe(longFrom.slice(0, 320));
  });
});

describe("computeRemoveMessageLinkPatch", () => {
  test("removes a matching link and returns the remaining array", () => {
    const a = existing({ message_id: 1001 });
    const b = existing({ message_id: 2002 });
    const c = existing({ message_id: 3003 });
    const patch = computeRemoveMessageLinkPatch([a, b, c], 2002);
    expect(patch).toEqual({ mail_links: [a, c] });
  });

  test("removing the last entry yields mail_links: null", () => {
    const only = existing({ message_id: 1001 });
    expect(computeRemoveMessageLinkPatch([only], 1001)).toEqual({
      mail_links: null,
    });
  });

  test("removing a non-existent message_id returns null", () => {
    const a = existing({ message_id: 1001 });
    expect(computeRemoveMessageLinkPatch([a], 9999)).toBeNull();
  });

  test("removing from an empty array returns null", () => {
    expect(computeRemoveMessageLinkPatch([], 1001)).toBeNull();
  });
});

describe("readMessageLinks", () => {
  test("returns [] when metadata is undefined", () => {
    expect(readMessageLinks(undefined)).toEqual([]);
  });

  test("returns [] when mail_links key is absent", () => {
    expect(readMessageLinks({})).toEqual([]);
  });

  test("returns [] when mail_links value is non-array", () => {
    expect(readMessageLinks({ mail_links: "nope" })).toEqual([]);
    expect(readMessageLinks({ mail_links: 42 })).toEqual([]);
    expect(readMessageLinks({ mail_links: null })).toEqual([]);
    expect(readMessageLinks({ mail_links: { foo: "bar" } })).toEqual([]);
  });

  test("returns the array when mail_links is a well-formed array", () => {
    const links = [existing({ message_id: 1001 }), existing({ message_id: 2002 })];
    expect(readMessageLinks({ mail_links: links })).toEqual(links);
  });

  test("returns the empty array when mail_links is an empty array", () => {
    expect(readMessageLinks({ mail_links: [] })).toEqual([]);
  });

  test("filters out malformed entries while keeping valid ones", () => {
    const good = existing({ message_id: 1001 });
    const arr = [
      null,
      undefined,
      good,
      { message_id: 0, subject: "x", from: "x", sent_at: "x", added_at: "x" },
      { message_id: 5, subject: "x", from: "x" },
      { message_id: 6, subject: 1, from: "x", sent_at: "x", added_at: "x" },
    ];
    expect(readMessageLinks({ mail_links: arr })).toEqual([good]);
  });
});
