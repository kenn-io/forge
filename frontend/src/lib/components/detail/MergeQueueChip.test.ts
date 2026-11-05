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

  it("shows only the queue position, with the queue state, enqueue time, and ETA in the tooltip", () => {
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

    const chip = screen.getByText("#2 in queue").closest(".kit-chip");
    expect(chip?.classList.contains("kit-chip--tone-muted")).toBe(true);
    const title = chip?.getAttribute("title") ?? "";
    expect(title).toContain("Queue state: Awaiting checks.");
    expect(title).toContain("Added 5m ago");
    expect(title).toContain("Estimated to merge in about 12 min.");
  });

  it.each(["queued", "awaiting_checks", "mergeable", "unmergeable", "locked", "waiting_for_capacity"])(
    "keeps the %s queue state calm, as the provider's own header does",
    (state) => {
      render(MergeQueueChip, {
        props: {
          entry: {
            position: 2,
            state,
            enqueued_at: "2026-07-10T15:05:00Z",
            observed_at: "2026-07-10T15:09:00Z",
          },
        },
      });

      const chip = screen.getByText("#2 in queue").closest(".kit-chip");
      expect(chip?.classList.contains("kit-chip--tone-muted")).toBe(true);
      expect(chip?.getAttribute("title")).not.toContain("Estimated");
    },
  );

  it("names an unmergeable entry's state in the tooltip", () => {
    render(MergeQueueChip, {
      props: {
        entry: {
          position: 2,
          state: "unmergeable",
          enqueued_at: "2026-07-10T15:05:00Z",
          observed_at: "2026-07-10T15:09:00Z",
        },
      },
    });

    expect(screen.getByText("#2 in queue").closest(".kit-chip")?.getAttribute("title")).toContain(
      "Queue state: Unmergeable.",
    );
  });

  it("does not repeat a plain queued state in the tooltip", () => {
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

    expect(screen.getByText("#3 in queue").closest(".kit-chip")?.getAttribute("title")).not.toContain("Queue state");
  });

  it("humanizes an unrecognized provider state in the tooltip", () => {
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

    expect(screen.getByText("#4 in queue").closest(".kit-chip")?.getAttribute("title")).toContain(
      "Queue state: Waiting for capacity.",
    );
  });

  it("renders nothing for an entry Forge just enqueued before sync observes its position", () => {
    const { container } = render(MergeQueueChip, {
      props: {
        entry: {
          position: 0,
          state: "queued",
          enqueued_at: "2026-07-10T15:09:00Z",
          observed_at: "2026-07-10T15:09:00Z",
        },
      },
    });

    expect(container.querySelector(".kit-chip")).toBeNull();
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

  it("names the failed merge group checks in the removal tooltip", () => {
    render(MergeQueueChip, {
      props: {
        removal: {
          removed_at: "2026-07-10T14:10:00Z",
          reason: "failed_checks",
          failed_checks: [{ name: "unit tests", url: "https://ci.example.com/3" }, { name: "ci/external" }],
        },
      },
    });

    const chip = screen.getByText("Removed from queue").closest(".kit-chip");
    expect(chip?.getAttribute("title")).toContain("Failed checks. Failed in the merge group: unit tests, ci/external.");
  });

  it("renders nothing without queue state", () => {
    const { container } = render(MergeQueueChip, { props: {} });

    expect(container.querySelector(".kit-chip")).toBeNull();
  });
});
