import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { PREvent } from "../../api/types.js";
import PRTimelineFilter from "./PRTimelineFilter.svelte";
import {
  activePRTimelineFilterCount,
  DEFAULT_PR_TIMELINE_FILTER,
  filterPREvents,
  loadPRTimelineFilter,
  savePRTimelineFilter,
  timelineEventBucket,
} from "./prTimelineFilter.js";

function event(overrides: Partial<PREvent>): PREvent {
  return {
    ID: 1,
    MergeRequestID: 1,
    PlatformID: null,
    EventType: "issue_comment",
    Author: "alice",
    Summary: "",
    Body: "body",
    MetadataJSON: "",
    CreatedAt: "2024-06-01T12:00:00Z",
    DedupeKey: "event-1",
    ...overrides,
  } as PREvent;
}

describe("prTimelineFilter", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  it("defaults to showing every bucket and bot activity", () => {
    expect(loadPRTimelineFilter()).toEqual(DEFAULT_PR_TIMELINE_FILTER);
  });

  it("persists valid filter state to localStorage", () => {
    savePRTimelineFilter({
      showMessages: false,
      showReplies: true,
      showCommitDetails: true,
      showEvents: true,
      showForcePushes: false,
      hideBots: true,
    });

    expect(loadPRTimelineFilter()).toEqual({
      showMessages: false,
      showReplies: true,
      showCommitDetails: true,
      showEvents: true,
      showForcePushes: false,
      hideBots: true,
    });
  });

  it("returns defaults for corrupt persisted JSON", () => {
    localStorage.setItem("middleman-pr-timeline-filter", "{");

    expect(loadPRTimelineFilter()).toEqual(DEFAULT_PR_TIMELINE_FILTER);
  });

  it("uses defaults for invalid persisted fields while preserving booleans", () => {
    localStorage.setItem(
      "middleman-pr-timeline-filter",
      JSON.stringify({
        showMessages: "false",
        showReplies: false,
        showCommitDetails: false,
        showEvents: 0,
        showForcePushes: true,
        hideBots: "true",
      }),
    );

    expect(loadPRTimelineFilter()).toEqual({
      showMessages: true,
      showReplies: false,
      showCommitDetails: false,
      showEvents: true,
      showForcePushes: true,
      hideBots: false,
    });
  });

  it("returns defaults when persisted JSON is not an object", () => {
    localStorage.setItem("middleman-pr-timeline-filter", JSON.stringify([]));
    expect(loadPRTimelineFilter()).toEqual(DEFAULT_PR_TIMELINE_FILTER);

    localStorage.setItem(
      "middleman-pr-timeline-filter",
      JSON.stringify("filter"),
    );
    expect(loadPRTimelineFilter()).toEqual(DEFAULT_PR_TIMELINE_FILTER);
  });

  it("returns defaults when localStorage reads throw", () => {
    const getItem = vi
      .spyOn(Storage.prototype, "getItem")
      .mockImplementation(() => {
        throw new Error("storage unavailable");
      });

    try {
      expect(loadPRTimelineFilter()).toEqual(DEFAULT_PR_TIMELINE_FILTER);
    } finally {
      getItem.mockRestore();
    }
  });

  it("does not throw when localStorage writes throw", () => {
    const setItem = vi
      .spyOn(Storage.prototype, "setItem")
      .mockImplementation(() => {
        throw new Error("storage full");
      });

    try {
      expect(() =>
        savePRTimelineFilter(DEFAULT_PR_TIMELINE_FILTER),
      ).not.toThrow();
    } finally {
      setItem.mockRestore();
    }
  });

  it("classifies event buckets", () => {
    expect(timelineEventBucket(event({ EventType: "issue_comment" }))).toBe(
      "messages",
    );
    expect(timelineEventBucket(event({ EventType: "review" }))).toBe(
      "messages",
    );
    expect(timelineEventBucket(event({ EventType: "commit" }))).toBe(
      "commitDetails",
    );
    expect(timelineEventBucket(event({ EventType: "force_push" }))).toBe(
      "forcePushes",
    );
    expect(timelineEventBucket(event({ EventType: "comment_deleted" }))).toBe(
      "events",
    );
    expect(timelineEventBucket(event({ EventType: "cross_referenced" }))).toBe(
      "events",
    );
    expect(timelineEventBucket(event({ EventType: "renamed_title" }))).toBe(
      "events",
    );
    expect(timelineEventBucket(event({ EventType: "base_ref_changed" }))).toBe(
      "events",
    );
    expect(timelineEventBucket(event({ EventType: "assigned" }))).toBe(
      "events",
    );
    expect(timelineEventBucket(event({ EventType: "unassigned" }))).toBe(
      "events",
    );
  });

  it("keeps commit title rows when commit details are disabled", () => {
    const events = [
      event({ ID: 1, EventType: "commit", Author: "alice" }),
      event({ ID: 2, EventType: "issue_comment", Author: "alice" }),
    ];

    expect(
      filterPREvents(events, {
        showMessages: true,
        showReplies: true,
        showCommitDetails: false,
        showEvents: true,
        showForcePushes: true,
        hideBots: false,
      }).map((item) => item.ID),
    ).toEqual([1, 2]);
  });

  it("filters by disabled buckets and bots", () => {
    const events = [
      event({ ID: 1, EventType: "issue_comment", Author: "alice" }),
      event({ ID: 2, EventType: "review", Author: "renovate[bot]" }),
      event({ ID: 3, EventType: "commit", Author: "alice" }),
      event({ ID: 4, EventType: "force_push", Author: "alice" }),
      event({ ID: 5, EventType: "base_ref_changed", Author: "alice" }),
      event({ ID: 6, EventType: "assigned", Author: "alice" }),
    ];

    expect(
      filterPREvents(events, {
        showMessages: true,
        showReplies: true,
        showCommitDetails: false,
        showEvents: true,
        showForcePushes: false,
        hideBots: true,
      }).map((item) => item.ID),
    ).toEqual([1, 3, 5, 6]);
  });

  it("filters threaded replies while keeping the root comment", () => {
    const events = [
      event({
        ID: 3,
        EventType: "issue_comment",
        Body: "new reply",
        ThreadID: "disc-1",
      }),
      event({
        ID: 2,
        EventType: "issue_comment",
        Body: "old reply",
        ThreadID: "disc-1",
      }),
      event({
        ID: 1,
        EventType: "issue_comment",
        Body: "root",
        ThreadID: "disc-1",
      }),
      event({
        ID: 4,
        EventType: "issue_comment",
        Body: "standalone",
      }),
    ];

    expect(
      filterPREvents(events, {
        showMessages: true,
        showReplies: false,
        showCommitDetails: true,
        showEvents: true,
        showForcePushes: true,
        hideBots: false,
      }).map((item) => item.ID),
    ).toEqual([1, 4]);
  });

  it("counts active timeline filters", () => {
    expect(activePRTimelineFilterCount(DEFAULT_PR_TIMELINE_FILTER)).toBe(0);
    expect(
      activePRTimelineFilterCount({
        showMessages: false,
        showReplies: false,
        showCommitDetails: true,
        showEvents: false,
        showForcePushes: true,
        hideBots: true,
      }),
    ).toBe(4);
  });
});

describe("PRTimelineFilter", () => {
  afterEach(() => {
    cleanup();
  });

  it("uses the shared filter dropdown trigger and emits changes", async () => {
    const onChange = vi.fn();
    render(PRTimelineFilter, {
      props: {
        filter: DEFAULT_PR_TIMELINE_FILTER,
        onChange,
      },
    });

    await fireEvent.click(screen.getByRole("button", { name: /filters/i }));
    await fireEvent.click(screen.getByRole("button", { name: /messages/i }));

    expect(onChange).toHaveBeenCalledWith({
      ...DEFAULT_PR_TIMELINE_FILTER,
      showMessages: false,
    });
    expect(document.querySelector(".filter-dropdown")).toBeTruthy();
  });

  it("shows active filter count and reset", async () => {
    const onChange = vi.fn();
    render(PRTimelineFilter, {
      props: {
        filter: {
          ...DEFAULT_PR_TIMELINE_FILTER,
          showCommitDetails: false,
          hideBots: true,
        },
        onChange,
      },
    });

    expect(screen.getByText("2")).toBeTruthy();
    await fireEvent.click(screen.getByRole("button", { name: /filters/i }));
    await fireEvent.click(screen.getByRole("button", { name: /show all/i }));

    expect(onChange).toHaveBeenCalledWith(DEFAULT_PR_TIMELINE_FILTER);
  });

  it("offers timeline filter labels", async () => {
    const onChange = vi.fn();
    render(PRTimelineFilter, {
      props: {
        filter: DEFAULT_PR_TIMELINE_FILTER,
        onChange,
      },
    });

    await fireEvent.click(screen.getByRole("button", { name: /filters/i }));

    expect(screen.getByRole("button", { name: /messages/i })).toBeTruthy();
    expect(screen.getByRole("button", { name: /replies/i })).toBeTruthy();
    expect(
      screen.getByRole("button", { name: /commit details/i }),
    ).toBeTruthy();
    expect(screen.getByRole("button", { name: /events/i })).toBeTruthy();
    expect(screen.getByRole("button", { name: /force pushes/i })).toBeTruthy();
    expect(
      screen.getByRole("button", { name: /hide bot activity/i }),
    ).toBeTruthy();
  });
});
