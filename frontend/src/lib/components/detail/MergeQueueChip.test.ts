import { cleanup, render, screen } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import MergeQueueChip from "./MergeQueueChip.svelte";

describe("MergeQueueChip", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-07-10T15:10:00Z"));
  });

  afterEach(() => {
    cleanup();
    vi.useRealTimers();
  });

  it("shows queue position and state, with enqueue time and ETA in the tooltip", () => {
    render(MergeQueueChip, {
      props: {
        entry: {
          position: 2,
          state: "awaiting_checks",
          enqueued_at: "2026-07-10T15:05:00Z",
          estimated_time_to_merge_seconds: 720,
          observed_at: "2026-07-10T15:09:00Z",
        },
      },
    });

    const chip = screen.getByText("Queued #2 · Awaiting checks").closest(".kit-chip");
    expect(chip?.classList.contains("kit-chip--tone-warning")).toBe(true);
    const title = chip?.getAttribute("title") ?? "";
    expect(title).toContain("Added 5m ago");
    expect(title).toContain("Estimated to merge in about 12 min.");
  });

  it("marks a queue entry that is ready to merge as success and omits an unknown ETA", () => {
    render(MergeQueueChip, {
      props: {
        entry: {
          position: 1,
          state: "mergeable",
          enqueued_at: "2026-07-10T15:05:00Z",
          observed_at: "2026-07-10T15:09:00Z",
        },
      },
    });

    const chip = screen.getByText("Queued #1 · Ready to merge").closest(".kit-chip");
    expect(chip?.classList.contains("kit-chip--tone-success")).toBe(true);
    expect(chip?.getAttribute("title")).not.toContain("Estimated");
  });

  it("omits the position of an entry Forge just enqueued before sync observes it", () => {
    render(MergeQueueChip, {
      props: {
        entry: {
          position: 0,
          state: "queued",
          enqueued_at: "2026-07-10T15:09:00Z",
          observed_at: "2026-07-10T15:09:00Z",
        },
      },
    });

    expect(screen.getByText("Queued")).toBeTruthy();
  });

  it("labels a plain queued entry without repeating the state", () => {
    render(MergeQueueChip, {
      props: {
        entry: {
          position: 3,
          state: "queued",
          enqueued_at: "2026-07-10T15:05:00Z",
          observed_at: "2026-07-10T15:09:00Z",
        },
      },
    });

    expect(screen.getByText("Queued #3")).toBeTruthy();
  });

  it("humanizes an unrecognized provider state instead of hiding the entry", () => {
    render(MergeQueueChip, {
      props: {
        entry: {
          position: 4,
          state: "waiting_for_capacity",
          enqueued_at: "2026-07-10T15:05:00Z",
          observed_at: "2026-07-10T15:09:00Z",
        },
      },
    });

    expect(screen.getByText("Queued #4 · Waiting for capacity")).toBeTruthy();
  });

  it("shows a danger chip with the humanized reason when the queue removed the pull request", () => {
    render(MergeQueueChip, {
      props: {
        removal: { removed_at: "2026-07-10T14:10:00Z", reason: "branch_protection_failure" },
      },
    });

    const chip = screen.getByText("Removed from queue").closest(".kit-chip");
    expect(chip?.classList.contains("kit-chip--tone-danger")).toBe(true);
    expect(chip?.getAttribute("title")).toContain("1h ago");
    expect(chip?.getAttribute("title")).toContain("Branch protection failure");
  });

  it("renders nothing without queue state", () => {
    const { container } = render(MergeQueueChip, { props: {} });

    expect(container.querySelector(".kit-chip")).toBeNull();
  });
});
