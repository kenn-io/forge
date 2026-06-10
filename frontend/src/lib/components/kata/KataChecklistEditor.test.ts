import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import type { KataTaskDetail } from "../../api/kata/taskTypes.js";

import KataChecklistEditor from "./KataChecklistEditor.svelte";

function makeIssue(
  checklist: KataTaskDetail["issue"]["metadata"]["checklist"] = [],
  overrides: Partial<KataTaskDetail["issue"]> = {},
): KataTaskDetail {
  return {
    issue: {
      id: 1,
      uid: "issue-1",
      project_id: 1,
      project_uid: "project-1",
      project_name: "Inbox",
      short_id: "I-1",
      qualified_id: "INBOX-1",
      title: "Ship the thing",
      body: "Body",
      status: "open",
      metadata: { checklist },
      revision: 1,
      author: "wes",
      created_at: "2026-06-01T12:00:00Z",
      updated_at: "2026-06-01T12:00:00Z",
      ...overrides,
    },
    comments: [],
    labels: [],
    links: [],
  };
}

describe("KataChecklistEditor", () => {
  afterEach(() => {
    cleanup();
  });

  it("stays hidden until an empty checklist is revealed", async () => {
    const { rerender } = render(KataChecklistEditor, {
      props: {
        issue: makeIssue(),
        revealed: false,
        onPatchMetadata: vi.fn(async () => true),
        onReveal: vi.fn(),
      },
    });

    expect(screen.queryByRole("region", { name: "Checklist" })).toBeNull();

    await rerender({
      issue: makeIssue(),
      revealed: true,
      onPatchMetadata: vi.fn(async () => true),
      onReveal: vi.fn(),
    });

    expect(screen.getByRole("region", { name: "Checklist" })).toBeTruthy();
  });

  it("full-replaces checklist metadata for add, toggle, and remove", async () => {
    const onPatchMetadata = vi.fn(async () => true);
    const onReveal = vi.fn();

    const { rerender } = render(KataChecklistEditor, {
      props: {
        issue: makeIssue([{ id: "item-1", text: "Send", done: false }]),
        revealed: false,
        onPatchMetadata,
        onReveal,
      },
    });

    await fireEvent.input(screen.getByLabelText("New checklist item"), {
      target: { value: "Confirm" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Add" }));

    expect(onPatchMetadata).toHaveBeenLastCalledWith("issue-1", {
      checklist: [
        { id: "item-1", text: "Send", done: false },
        { id: expect.any(String), text: "Confirm", done: false },
      ],
    });

    await rerender({
      issue: makeIssue([
        { id: "item-1", text: "Send", done: false },
        { id: "item-2", text: "Confirm", done: false },
      ]),
      revealed: false,
      onPatchMetadata,
      onReveal,
    });
    await fireEvent.click(screen.getByLabelText("Send"));

    expect(onPatchMetadata).toHaveBeenLastCalledWith("issue-1", {
      checklist: [
        { id: "item-1", text: "Send", done: true },
        { id: "item-2", text: "Confirm", done: false },
      ],
    });

    await rerender({
      issue: makeIssue([{ id: "item-1", text: "Send", done: false }]),
      revealed: false,
      onPatchMetadata,
      onReveal,
    });
    await fireEvent.click(screen.getByRole("button", { name: "Remove Send" }));

    expect(onPatchMetadata).toHaveBeenLastCalledWith("issue-1", { checklist: [] });
    await waitFor(() => {
      expect(onReveal).toHaveBeenCalledTimes(1);
    });
  });

  it("clears the add-item draft when the selected task changes", async () => {
    const { rerender } = render(KataChecklistEditor, {
      props: {
        issue: makeIssue(),
        revealed: true,
        onPatchMetadata: vi.fn(async () => true),
        onReveal: vi.fn(),
      },
    });

    await fireEvent.input(screen.getByLabelText("New checklist item"), {
      target: { value: "Leaked draft" },
    });
    await rerender({
      issue: makeIssue([], { uid: "issue-2", short_id: "I-2", qualified_id: "INBOX-2" }),
      revealed: true,
      onPatchMetadata: vi.fn(async () => true),
      onReveal: vi.fn(),
    });

    expect((screen.getByLabelText("New checklist item") as HTMLInputElement).value).toBe("");
  });
});
