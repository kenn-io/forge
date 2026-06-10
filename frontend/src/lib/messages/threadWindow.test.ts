import { describe, expect, test } from "vite-plus/test";
import type { MessageSummary } from "../api/messages/types";
import { selectInclusiveWindow } from "./threadWindow";

function msg(id: number, sent_at: string): MessageSummary {
  return {
    id,
    conversation_id: 1,
    subject: `m${id}`,
    from: "a@example.com",
    to: [],
    cc: [],
    bcc: [],
    sent_at,
    snippet: "",
    labels: [],
    has_attachments: false,
    size_bytes: 0,
    deleted_at: null,
  };
}

function buildSorted(n: number): MessageSummary[] {
  const base = Date.UTC(2026, 4, 15, 0, 0, 0);
  return Array.from({ length: n }, (_, i) => msg(i + 1, new Date(base + i * 60_000).toISOString()));
}

describe("selectInclusiveWindow", () => {
  test("returns the full thread unchanged when length is at or below the cap", () => {
    const sorted = buildSorted(10);
    const { messages, truncated } = selectInclusiveWindow(sorted, 5, 50);
    expect(truncated).toBe(false);
    expect(messages).toEqual(sorted);
  });

  test("returns the recent cap when length exceeds it and selected sits in the tail", () => {
    const sorted = buildSorted(60);
    const { messages, truncated } = selectInclusiveWindow(sorted, 60, 50);
    expect(truncated).toBe(true);
    expect(messages.length).toBe(50);
    expect(messages[0]!.id).toBe(11);
    expect(messages[49]!.id).toBe(60);
  });

  test("swaps the oldest of the recent cap for selected when selected is older", () => {
    const sorted = buildSorted(60);
    const { messages, truncated } = selectInclusiveWindow(sorted, 5, 50);
    expect(truncated).toBe(true);
    expect(messages.length).toBe(50);
    expect(messages.find((m) => m.id === 5)).toBeDefined();
    for (let i = 1; i < messages.length; i++) {
      expect(messages[i]!.sent_at >= messages[i - 1]!.sent_at).toBe(true);
    }
    expect(messages.find((m) => m.id === 11)).toBeUndefined();
    for (let id = 12; id <= 60; id++) {
      expect(messages.find((m) => m.id === id)).toBeDefined();
    }
  });

  test("returns recent cap when selectedId is not found in the sorted list", () => {
    const sorted = buildSorted(60);
    const { messages, truncated } = selectInclusiveWindow(sorted, 999, 50);
    expect(truncated).toBe(true);
    expect(messages.length).toBe(50);
    expect(messages[0]!.id).toBe(11);
    expect(messages[49]!.id).toBe(60);
  });

  test("boundary: cap equals length means no truncation", () => {
    const sorted = buildSorted(50);
    const { messages, truncated } = selectInclusiveWindow(sorted, 25, 50);
    expect(truncated).toBe(false);
    expect(messages).toEqual(sorted);
  });

  test("empty thread returns empty window with truncated false", () => {
    const { messages, truncated } = selectInclusiveWindow([], 1, 50);
    expect(messages).toEqual([]);
    expect(truncated).toBe(false);
  });

  test("default cap is 50 when omitted", () => {
    const sorted = buildSorted(51);
    const { messages, truncated } = selectInclusiveWindow(sorted, 51);
    expect(truncated).toBe(true);
    expect(messages.length).toBe(50);
  });
});
