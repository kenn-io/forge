import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";

import { STORES_KEY } from "../../context.js";
import DiffReviewDraftTray from "./DiffReviewDraftTray.svelte";

function renderTray(publishResult: boolean) {
  const publish = vi.fn(() => Promise.resolve(publishResult));
  const diffReviewDraft = {
    getComments: () => [{
      id: "1",
      body: "Draft note",
      path: "src/foo.ts",
      line: 12,
    }],
    getDraft: () => ({
      supported_actions: ["comment"],
    }),
    isSubmitting: () => false,
    getError: () => publishResult ? null : "publish failed",
    publish,
    discard: () => Promise.resolve(true),
    deleteComment: () => Promise.resolve(true),
  };
  const rendered = render(DiffReviewDraftTray, {
    context: new Map([[STORES_KEY, { diffReviewDraft }]]),
  });
  return { ...rendered, publish };
}

describe("DiffReviewDraftTray", () => {
  afterEach(() => {
    cleanup();
  });

  it("keeps review summary text when publishing fails", async () => {
    const { publish } = renderTray(false);
    const summary = screen.getByPlaceholderText("Review summary") as HTMLTextAreaElement;

    await fireEvent.input(summary, { target: { value: "Keep this summary" } });
    await fireEvent.click(screen.getByRole("button", { name: "Publish review" }));

    expect(publish).toHaveBeenCalledWith("comment", "Keep this summary");
    expect(summary.value).toBe("Keep this summary");
  });
});
