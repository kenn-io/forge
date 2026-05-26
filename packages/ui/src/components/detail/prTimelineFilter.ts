import type { PREvent } from "../../api/types.js";

export interface PRTimelineFilterState {
  showMessages: boolean;
  showReplies: boolean;
  showCommitDetails: boolean;
  showEvents: boolean;
  showForcePushes: boolean;
  hideBots: boolean;
}

export type PRTimelineEventBucket =
  | "messages"
  | "commitDetails"
  | "events"
  | "forcePushes";

export const PR_TIMELINE_FILTER_STORAGE_KEY = "middleman-pr-timeline-filter";

export const DEFAULT_PR_TIMELINE_FILTER: PRTimelineFilterState = {
  showMessages: true,
  showReplies: true,
  showCommitDetails: true,
  showEvents: true,
  showForcePushes: true,
  hideBots: false,
};

const BOT_SUFFIXES = ["[bot]", "-bot", "bot"];

export function isBotAuthor(author: string): boolean {
  const lower = author.toLowerCase();
  return BOT_SUFFIXES.some((suffix) => lower.endsWith(suffix));
}

function eventSortValue(event: PREvent): number {
  const timestamp = Date.parse(event.CreatedAt);
  return Number.isFinite(timestamp) ? timestamp : 0;
}

function isEarlierEvent(a: PREvent, b: PREvent): boolean {
  return eventSortValue(a) < eventSortValue(b) ||
    (eventSortValue(a) === eventSortValue(b) && a.ID < b.ID);
}

function reviewThreadID(event: PREvent): string | null {
  if (!("diff_thread" in event)) return null;
  const thread = event.diff_thread as { id?: unknown } | undefined;
  return typeof thread?.id === "string" && thread.id.length > 0
    ? thread.id
    : null;
}

function timelineThreadID(event: PREvent): string | null {
  return event.ThreadID || reviewThreadID(event);
}

export function timelineEventBucket(event: PREvent): PRTimelineEventBucket {
  switch (event.EventType) {
    case "issue_comment":
    case "review":
    case "review_comment":
      return "messages";
    case "commit":
      return "commitDetails";
    case "force_push":
      return "forcePushes";
    default:
      return "events";
  }
}

function booleanOrDefault(value: unknown, fallback: boolean): boolean {
  return typeof value === "boolean" ? value : fallback;
}

function normalizeFilter(value: unknown): PRTimelineFilterState {
  const persisted =
    value !== null && typeof value === "object" && !Array.isArray(value)
      ? (value as Record<string, unknown>)
      : {};

  return {
    showMessages: booleanOrDefault(
      persisted.showMessages,
      DEFAULT_PR_TIMELINE_FILTER.showMessages,
    ),
    showCommitDetails: booleanOrDefault(
      persisted.showCommitDetails,
      DEFAULT_PR_TIMELINE_FILTER.showCommitDetails,
    ),
    showReplies: booleanOrDefault(
      persisted.showReplies,
      DEFAULT_PR_TIMELINE_FILTER.showReplies,
    ),
    showEvents: booleanOrDefault(
      persisted.showEvents,
      DEFAULT_PR_TIMELINE_FILTER.showEvents,
    ),
    showForcePushes: booleanOrDefault(
      persisted.showForcePushes,
      DEFAULT_PR_TIMELINE_FILTER.showForcePushes,
    ),
    hideBots: booleanOrDefault(
      persisted.hideBots,
      DEFAULT_PR_TIMELINE_FILTER.hideBots,
    ),
  };
}

export function loadPRTimelineFilter(): PRTimelineFilterState {
  try {
    const raw = localStorage.getItem(PR_TIMELINE_FILTER_STORAGE_KEY);
    if (!raw) return DEFAULT_PR_TIMELINE_FILTER;
    return normalizeFilter(JSON.parse(raw) as unknown);
  } catch {
    return DEFAULT_PR_TIMELINE_FILTER;
  }
}

export function savePRTimelineFilter(filter: PRTimelineFilterState): void {
  try {
    localStorage.setItem(
      PR_TIMELINE_FILTER_STORAGE_KEY,
      JSON.stringify(filter),
    );
  } catch {
    // localStorage can be unavailable in private browsing or embedded contexts.
  }
}

export function filterPREvents(
  events: PREvent[],
  filter: PRTimelineFilterState,
): PREvent[] {
  const threadRoots = new Map<string, PREvent>();
  for (const event of events) {
    const threadID = timelineThreadID(event);
    if (
      !threadID ||
      !["issue_comment", "review", "review_comment"].includes(event.EventType)
    ) {
      continue;
    }
    const currentRoot = threadRoots.get(threadID);
    if (currentRoot === undefined || isEarlierEvent(event, currentRoot)) {
      threadRoots.set(threadID, event);
    }
  }

  return events.filter((event) => {
    const threadID = timelineThreadID(event);
    if (filter.hideBots && isBotAuthor(event.Author)) return false;
    if (
      !filter.showReplies &&
      threadID &&
      threadRoots.get(threadID)?.ID !== event.ID
    ) {
      return false;
    }
    switch (timelineEventBucket(event)) {
      case "messages":
        return filter.showMessages;
      case "commitDetails":
        return true;
      case "events":
        return filter.showEvents;
      case "forcePushes":
        return filter.showForcePushes;
    }
  });
}

export function activePRTimelineFilterCount(
  filter: PRTimelineFilterState,
): number {
  return [
    !filter.showMessages,
    !filter.showReplies,
    !filter.showCommitDetails,
    !filter.showEvents,
    !filter.showForcePushes,
    filter.hideBots,
  ].filter(Boolean).length;
}
