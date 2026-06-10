import { cleanup, fireEvent, render, screen, within } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import type { KataTaskDetail } from "../../api/kata/taskTypes.js";

import KataIssueOverflowMenu from "./KataIssueOverflowMenu.svelte";

function makeIssue(status: "open" | "closed" = "open"): KataTaskDetail {
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
      status,
      metadata: {},
      revision: 1,
      author: "wes",
      created_at: "2026-06-01T12:00:00Z",
      updated_at: "2026-06-01T12:00:00Z",
    },
    comments: [],
    labels: [],
    links: [],
  };
}

describe("KataIssueOverflowMenu", () => {
  afterEach(() => {
    cleanup();
  });

  it("routes menu actions to their callbacks", async () => {
    const onAddChecklist = vi.fn();
    const onCreateRecurrence = vi.fn();

    render(KataIssueOverflowMenu, {
      props: {
        issue: makeIssue(),
        hasChecklist: false,
        hasRecurrence: false,
        onAddChecklist,
        onCreateRecurrence,
        onDeleteIssue: vi.fn(async () => true),
      },
    });

    await fireEvent.click(screen.getByRole("button", { name: "More actions" }));
    await fireEvent.click(screen.getByRole("menuitem", { name: "Add checklist" }));
    expect(onAddChecklist).toHaveBeenCalledTimes(1);

    await fireEvent.click(screen.getByRole("button", { name: "More actions" }));
    await fireEvent.click(screen.getByRole("menuitem", { name: "Mark as recurring..." }));
    expect(onCreateRecurrence).toHaveBeenCalledTimes(1);
  });

  it("confirms delete through the menu", async () => {
    const onDeleteIssue = vi.fn(async () => true);

    render(KataIssueOverflowMenu, {
      props: {
        issue: makeIssue(),
        hasChecklist: true,
        hasRecurrence: true,
        onAddChecklist: vi.fn(),
        onCreateRecurrence: vi.fn(),
        onDeleteIssue,
      },
    });

    await fireEvent.click(screen.getByRole("button", { name: "More actions" }));
    await fireEvent.click(screen.getByRole("menuitem", { name: "Delete issue" }));

    const dialog = screen.getByRole("dialog", { name: "Delete issue" });
    await fireEvent.click(within(dialog).getByRole("button", { name: "Delete" }));

    expect(onDeleteIssue).toHaveBeenCalledTimes(1);
  });

  it("hides the trigger when no actions are available", () => {
    render(KataIssueOverflowMenu, {
      props: {
        issue: makeIssue("closed"),
        hasChecklist: true,
        hasRecurrence: true,
        onAddChecklist: vi.fn(),
        onCreateRecurrence: vi.fn(),
        onDeleteIssue: vi.fn(async () => true),
      },
    });

    expect(screen.queryByRole("button", { name: "More actions" })).toBeNull();
  });
});
