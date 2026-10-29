import type { ChipTone } from "@kenn-io/kit-ui";
import { formatRelativeTime } from "@kenn-io/kit-ui";

import type { PullDetail, PullRequest } from "../../api/types.js";
import { localDateTimeLabel } from "../../utils/time.js";

export type MergeQueueEntry = NonNullable<PullRequest["merge_queue"]>;
export type MergeQueueRemoval = NonNullable<PullRequest["merge_queue_removal"]>;
export type MergeQueueTarget = NonNullable<PullDetail["merge_queue_target"]>;

interface MergeQueueStatePresentation {
  readonly label: string;
  readonly tone: ChipTone;
}

const knownStates: Readonly<Record<string, MergeQueueStatePresentation>> = {
  queued: { label: "Queued", tone: "warning" },
  awaiting_checks: { label: "Awaiting checks", tone: "warning" },
  mergeable: { label: "Ready to merge", tone: "success" },
  unmergeable: { label: "Unmergeable", tone: "danger" },
  locked: { label: "Locked", tone: "merged" },
};

/** Converts a raw provider token such as `branch_protection_failure` to sentence case. */
export function humanizeProviderToken(raw: string): string {
  const words = raw.trim().replace(/[_-]+/g, " ").replace(/\s+/g, " ").toLowerCase();
  if (words === "") return "";
  return words.charAt(0).toUpperCase() + words.slice(1);
}

/** Human-readable reason the merge queue removed a pull request. */
export function mergeQueueRemovalReason(removal: MergeQueueRemoval): string {
  return humanizeProviderToken(removal.reason) || "No reason given";
}

/** Presentation for a queue entry state; unknown provider states fall back to a humanized amber label. */
export function mergeQueueState(state: string): MergeQueueStatePresentation {
  const key = state.trim().toLowerCase();
  return knownStates[key] ?? { label: humanizeProviderToken(key) || "Queued", tone: "warning" };
}

/** Rough duration label for a provider ETA in seconds. */
export function formatMergeQueueEta(seconds: number): string {
  if (seconds < 60) return "under a minute";
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `about ${minutes} min`;
  const hours = Math.floor(minutes / 60);
  const rest = minutes % 60;
  return rest === 0 ? `about ${hours} h` : `about ${hours} h ${rest} min`;
}

/** True once sync has observed the entry's position; Forge-enqueued entries start at 0. */
export function hasMergeQueuePosition(entry: MergeQueueEntry): boolean {
  return entry.position > 0;
}

/** Tooltip details for a queue entry; the queue chip already shows its position and state. */
export function mergeQueueEntryDescription(entry: MergeQueueEntry): string {
  const parts = [`Added ${formatRelativeTime(entry.enqueued_at)} (${localDateTimeLabel(entry.enqueued_at)}).`];
  if (entry.estimated_time_to_merge_seconds !== undefined && entry.estimated_time_to_merge_seconds > 0) {
    parts.push(`Estimated to merge in ${formatMergeQueueEta(entry.estimated_time_to_merge_seconds)}.`);
  }
  return parts.join(" ");
}

/** Accessible label for the sidebar queue indicator. */
export function mergeQueueIndicatorLabel(
  entry: MergeQueueEntry | undefined,
  removal: MergeQueueRemoval | undefined,
): string | undefined {
  if (entry) return hasMergeQueuePosition(entry) ? `In merge queue, position ${entry.position}` : "In merge queue";
  if (removal) return `Removed from merge queue: ${mergeQueueRemovalReason(removal)}`;
  return undefined;
}
