import { cleanup, fireEvent, render, screen, within } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import type { KataTaskDetail } from "../../api/kata/taskTypes.js";

import KataIssueActions from "./KataIssueActions.svelte";

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

describe("KataIssueActions", () => {
  afterEach(() => {
    cleanup();
  });

  it("submits the selected close reason and completion note", async () => {
    const onCloseIssue = vi.fn(async () => true);

    render(KataIssueActions, {
      props: {
        issue: makeIssue(),
        onCloseIssue,
        onReopenIssue: vi.fn(),
      },
    });

    await fireEvent.click(screen.getByRole("button", { name: "Complete" }));
    const dialog = screen.getByRole("dialog", { name: "Complete task" });

    await fireEvent.click(within(dialog).getByRole("radio", { name: /Won't do/ }));
    await fireEvent.input(within(dialog).getByLabelText(/Completion note/), {
      target: { value: "No longer needed." },
    });
    await fireEvent.click(within(dialog).getByRole("button", { name: "Complete" }));

    expect(onCloseIssue).toHaveBeenCalledWith("wontfix", "No longer needed.");
  });

  it("reopens a closed task", async () => {
    const onReopenIssue = vi.fn();

    render(KataIssueActions, {
      props: {
        issue: makeIssue("closed"),
        onCloseIssue: vi.fn(),
        onReopenIssue,
      },
    });

    await fireEvent.click(screen.getByRole("button", { name: "Reopen" }));

    expect(onReopenIssue).toHaveBeenCalledTimes(1);
  });
});
