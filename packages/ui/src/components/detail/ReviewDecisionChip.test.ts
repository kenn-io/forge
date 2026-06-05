import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it } from "vite-plus/test";
import type { PREvent } from "../../api/types.js";
import ReviewDecisionChip from "./ReviewDecisionChip.svelte";

function reviewEvent(author: string, summary = "APPROVED", createdAt = "2026-05-01T12:00:00Z"): PREvent {
  return {
    ID: Math.floor(Math.random() * 1_000_000),
    MergeRequestID: 1,
    PlatformID: 1,
    PlatformExternalID: "",
    EventType: "review",
    Author: author,
    Summary: summary,
    Body: "",
    MetadataJSON: "",
    CreatedAt: createdAt,
    DedupeKey: `review-${author}-${summary}-${createdAt}`,
  };
}

describe("ReviewDecisionChip", () => {
  afterEach(() => {
    cleanup();
  });

  it("shows approval count and expands approver names", async () => {
    render(ReviewDecisionChip, {
      props: {
        decision: "APPROVED",
        events: [
          reviewEvent("alice", "APPROVED", "2026-05-01T12:00:00Z"),
          reviewEvent("bob", "APPROVED", "2026-05-01T11:59:00Z"),
        ],
      },
    });

    const trigger = screen.getByRole("button", {
      name: "APPROVED (2)",
    });
    await fireEvent.click(trigger);

    const popup = document.querySelector(".approval-popup");
    expect(popup?.textContent).toContain("alice");
    expect(popup?.textContent).toContain("bob");

    await fireEvent.mouseDown(document.body);

    expect(document.querySelector(".approval-popup")).toBeNull();
  });
});
