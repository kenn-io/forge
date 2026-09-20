import type { ChipTone } from "@kenn-io/kit-ui";

export interface WorkflowStatusPresentation {
  label: string;
  tone: ChipTone;
  // Sort rank: attention first (failed, then active), settled last.
  rank: number;
}

const FAILED = new Set(["failure", "failed", "timed_out", "startup_failure"]);
const CANCELED = new Set(["cancelled", "canceled"]);
const SKIPPED = new Set(["skipped", "neutral", "stale"]);
const RUNNING = new Set(["in_progress", "running"]);
const WAITING = new Set(["queued", "pending", "waiting", "requested", "created", "manual", "scheduled"]);

// Providers report a lifecycle status plus, once settled, a conclusion. The
// conclusion is the outcome a maintainer scans for, so it wins when present.
export function workflowStatusPresentation(status: string, conclusion: string): WorkflowStatusPresentation {
  const value = (conclusion || status).trim().toLowerCase();
  const label = value.replaceAll("_", " ") || "unknown";
  if (FAILED.has(value)) return { label, tone: "danger", rank: 0 };
  if (value === "action_required") return { label, tone: "warning", rank: 1 };
  if (RUNNING.has(value)) return { label, tone: "info", rank: 2 };
  if (WAITING.has(value)) return { label, tone: "warning", rank: 3 };
  if (CANCELED.has(value)) return { label, tone: "canceled", rank: 4 };
  if (value === "success") return { label, tone: "success", rank: 5 };
  if (SKIPPED.has(value)) return { label, tone: "muted", rank: 6 };
  return { label, tone: "muted", rank: 7 };
}
