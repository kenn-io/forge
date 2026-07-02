import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";

import { STORES_KEY } from "../../context.js";
import DiffReviewDraftTray from "./DiffReviewDraftTray.svelte";

function renderTray(publishResult: boolean) {
  const publish = vi.fn(() => Promise.resolve(publishResult));
  const editComment = vi.fn(() => Promise.resolve(true));
  const diffReviewDraft = {
    getComments: () => [
      {
        id: "1",
        body: "Draft note",
        path: "src/foo.ts",
        line: 12,
      },
    ],
    getDraft: () => ({
      supported_actions: ["comment"],
    }),
    isSubmitting: () => false,
    getError: () => (publishResult ? null : "publish failed"),
    publish,
    discard: () => Promise.resolve(true),
    deleteComment: () => Promise.resolve(true),
    editComment,
  };
  const rendered = render(DiffReviewDraftTray, {
    context: new Map([[STORES_KEY, { diffReviewDraft }]]),
  });
  return { ...rendered, editComment, publish };
}

describe("DiffReviewDraftTray", () => {
  afterEach(() => {
    cleanup();
  });

  it("keeps review summary text when publishing fails", async () => {
    const { publish } = renderTray(false);
    const summary = screen.getByPlaceholderText("Review summary") as HTMLTextAreaElement;

    await fireEvent.input(summary, {
      target: { value: "Keep this summary" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Publish review" }));

    expect(publish).toHaveBeenCalledWith("comment", "Keep this summary");
    expect(summary.value).toBe("Keep this summary");
  });

  it("lets a draft comment body be edited before publishing", async () => {
    const { editComment } = renderTray(true);

    await fireEvent.click(screen.getByRole("button", { name: "Edit draft comment" }));
    const editor = screen.getByLabelText("Draft comment body") as HTMLTextAreaElement;
    expect(editor.value).toBe("Draft note");

    await fireEvent.input(editor, {
      target: { value: "Updated draft note" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Save draft comment" }));

    expect(editComment).toHaveBeenCalledWith(
      expect.objectContaining({ id: "1", body: "Draft note" }),
      "Updated draft note",
    );
    expect(screen.queryByLabelText("Draft comment body")).toBeNull();
  });
});
