import type { CICheck } from "../api/types.js";

export type CIBucket = "failed" | "pending" | "passed" | "skipped" | "unknown";

const ACTIVE_STATUSES = new Set([
  "in_progress",
  "queued",
  "pending",
  "waiting",
]);

const FAILED_CONCLUSIONS = new Set([
  "failure",
  "cancelled",
  "timed_out",
  "action_required",
  "stale",
  "startup_failure",
]);

const SKIPPED_CONCLUSIONS = new Set(["skipped", "neutral"]);

export function bucketForCheck(check: CICheck): CIBucket {
  if (ACTIVE_STATUSES.has(check.status)) return "pending";
  if (FAILED_CONCLUSIONS.has(check.conclusion)) return "failed";
  if (check.conclusion === "success") return "passed";
  if (SKIPPED_CONCLUSIONS.has(check.conclusion)) return "skipped";
  if (check.conclusion !== "") return "unknown";
  return "pending";
}

export interface CIBucketedChecks {
  failed: CICheck[];
  pending: CICheck[];
  passed: CICheck[];
  skipped: CICheck[];
  unknown: CICheck[];
  all: CICheck[];
  longestCompletedDurationSeconds: number | undefined;
}

export function bucketCIChecks(checks: CICheck[]): CIBucketedChecks {
  const result: CIBucketedChecks = {
    failed: [],
    pending: [],
    passed: [],
    skipped: [],
    unknown: [],
    all: checks,
    longestCompletedDurationSeconds: undefined,
  };
  let longest: number | undefined;
  for (const check of checks) {
    result[bucketForCheck(check)].push(check);
    if (
      check.status === "completed" &&
      typeof check.duration_seconds === "number" &&
      Number.isFinite(check.duration_seconds) &&
      check.duration_seconds >= 0
    ) {
      if (longest === undefined || check.duration_seconds > longest) {
        longest = check.duration_seconds;
      }
    }
  }
  result.longestCompletedDurationSeconds = longest;
  return result;
}

export interface ParsedCIChecks {
  checks: CICheck[];
  error: Error | null;
}

function coerceString(value: unknown): string {
  if (value == null) return "";
  return String(value);
}

function coerceDuration(value: unknown): number | undefined {
  if (value == null) return undefined;
  const n = typeof value === "number" ? value : Number(value);
  if (!Number.isFinite(n) || n < 0) return undefined;
  return n;
}

export function parseCIChecks(json: string): ParsedCIChecks {
  if (json.trim() === "") return { checks: [], error: null };
  let parsed: unknown;
  try {
    parsed = JSON.parse(json);
  } catch (err) {
    return {
      checks: [],
      error: err instanceof Error ? err : new Error(String(err)),
    };
  }
  if (!Array.isArray(parsed)) {
    return { checks: [], error: new Error("CIChecksJSON: payload is not an array") };
  }
  const checks: CICheck[] = [];
  for (const elem of parsed) {
    if (typeof elem !== "object" || elem === null) {
      return {
        checks: [],
        error: new Error("CIChecksJSON: payload contains a non-object element"),
      };
    }
    const raw = elem as Record<string, unknown>;
    const check: CICheck = {
      name: coerceString(raw.name),
      status: coerceString(raw.status),
      conclusion: coerceString(raw.conclusion),
      url: coerceString(raw.url),
      app: coerceString(raw.app),
    };
    if (typeof raw.required === "boolean") check.required = raw.required;
    const duration = coerceDuration(raw.duration_seconds);
    if (duration !== undefined) check.duration_seconds = duration;
    checks.push(check);
  }
  return { checks, error: null };
}

// Render-safe projection of a parse error. Locally-created shape errors
// carry a stable "CIChecksJSON: " prefix and are forwarded intact;
// everything else collapses to a single generic string so native
// JSON.parse messages (which can embed input fragments) never reach the
// DOM.
export function safeDiagnosticText(error: Error): string {
  return error.message.startsWith("CIChecksJSON: ") ? error.message : "Malformed JSON";
}
