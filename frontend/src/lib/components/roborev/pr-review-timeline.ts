import type { PREvent } from "../../api/types.js";
import type { ChipTone } from "@kenn-io/kit-ui";
import type * as RoborevModels from "../../api/roborev/generated/models/index.js";

type ReviewJob = RoborevModels.ReviewJob;

/** Marker roborev puts on every pull request comment it posts. */
export const ROBOREV_PR_COMMENT_MARKER = "<!-- roborev-pr-comment -->";

// Job types that produce a review of code. Fix, task, insights, compact, and
// classify jobs run on the same branch but are not reviews.
const REVIEW_JOB_TYPES = new Set(["review", "range", "dirty", "synthesis"]);

export type RoborevVerdict = "findings" | "clean" | "running" | "failed" | "unknown";
export type RoborevReviewState = "current" | "superseded" | "addressed";

interface TimelineEntryBase {
  readonly key: string;
  readonly at: string;
  /** Reviewed commit SHA, full or abbreviated; null for uncommitted changes. */
  readonly commit: string | null;
  readonly verdict: RoborevVerdict;
  /** Findings at or above the review's severity threshold; null when unknown. */
  readonly findings: number | null;
  readonly state: RoborevReviewState | null;
}

export interface LocalTimelineEntry extends TimelineEntryBase {
  readonly source: "local";
  readonly jobId: number;
}

export interface ProviderTimelineEntry extends TimelineEntryBase {
  readonly source: "provider";
  readonly author: string;
  readonly body: string;
  readonly url: string;
}

export type RoborevTimelineEntry = LocalTimelineEntry | ProviderTimelineEntry;

const HEADING = /^## roborev: (.+?)(?: \(`([0-9a-f]{7,40})`\))?\s*$/m;
const FINDINGS_VERDICT = /\*\*Verdict:\*\* Changes require fixes for (\d+) findings?\./;
const OMITTED_FINDINGS = /^(\d+) more findings? in the full review/m;
const CLEAN_VERDICT = /\*\*Verdict:\*\* No (?:issues found|findings at or above \w+ severity)\./;

function sameCommit(a: string, b: string): boolean {
  const left = a.toLowerCase();
  const right = b.toLowerCase();
  return left.startsWith(right) || right.startsWith(left);
}

function reviewState(commit: string | null, headSha: string, closed: boolean): RoborevReviewState | null {
  if (closed) return "addressed";
  if (commit === null || headSha === "") return null;
  return sameCommit(commit, headSha) ? "current" : "superseded";
}

function providerVerdict(title: string, body: string): Pick<TimelineEntryBase, "verdict" | "findings"> {
  const findings = FINDINGS_VERDICT.exec(body);
  if (findings) {
    const omitted = OMITTED_FINDINGS.exec(body);
    return { verdict: "findings", findings: Number(findings[1]) + (omitted ? Number(omitted[1]) : 0) };
  }
  if (CLEAN_VERDICT.test(body) || title === "Pass" || title.startsWith("Review Passed")) {
    return { verdict: "clean", findings: 0 };
  }
  if (title === "Fail") return { verdict: "findings", findings: null };
  if (title.startsWith("Review Failed") || title.startsWith("Review Skipped")) {
    return { verdict: "failed", findings: null };
  }
  return { verdict: "unknown", findings: null };
}

/** Reads a review roborev posted as a pull request comment, or null for any other event. */
export function providerReviewEntry(event: PREvent, headSha: string): ProviderTimelineEntry | null {
  if (event.EventType !== "issue_comment" || !event.Body.includes(ROBOREV_PR_COMMENT_MARKER)) return null;
  const heading = HEADING.exec(event.Body);
  if (!heading) return null;
  const commit = heading[2] ?? null;
  return {
    source: "provider",
    key: `provider:${event.ID}`,
    at: event.CreatedAt,
    commit,
    ...providerVerdict(heading[1] ?? "", event.Body),
    state: reviewState(commit, headSha, false),
    author: event.Author,
    body: event.Body.replace(ROBOREV_PR_COMMENT_MARKER, "").trim(),
    url: event.DirectURL,
  };
}

function jobCommit(job: ReviewJob): string | null {
  if (job.job_type === "dirty") return null;
  const ref = job.git_ref.includes("..") ? job.git_ref.slice(job.git_ref.lastIndexOf("..") + 2) : job.git_ref;
  return /^[0-9a-f]{7,40}$/i.test(ref) ? ref : null;
}

function jobVerdict(job: ReviewJob): Pick<TimelineEntryBase, "verdict" | "findings"> {
  const counts = job.finding_counts;
  const findings = counts ? counts.critical + counts.high + counts.medium + counts.low : null;
  if (job.status === "queued" || job.status === "running") return { verdict: "running", findings: null };
  if (job.status !== "done") return { verdict: "failed", findings: null };
  if (job.verdict === "F" || job.verdict === "fail") return { verdict: "findings", findings };
  if (job.verdict === "P" || job.verdict === "pass") return { verdict: "clean", findings };
  return { verdict: "unknown", findings };
}

/** Maps a local roborev job to a timeline entry, or null when the job is not a review. */
export function localReviewEntry(job: ReviewJob, headSha: string): LocalTimelineEntry | null {
  if (!REVIEW_JOB_TYPES.has(job.job_type)) return null;
  const commit = jobCommit(job);
  return {
    source: "local",
    key: `local:${job.id}`,
    jobId: job.id,
    at: job.finished_at ?? job.started_at ?? job.enqueued_at,
    commit,
    ...jobVerdict(job),
    state: reviewState(commit, headSha, job.closed === true),
  };
}

/** Merges both review sources, oldest first. */
export function buildRoborevTimeline(
  events: ReadonlyArray<PREvent>,
  jobs: ReadonlyArray<ReviewJob>,
  headSha: string,
): RoborevTimelineEntry[] {
  const entries: RoborevTimelineEntry[] = [];
  for (const event of events) {
    const entry = providerReviewEntry(event, headSha);
    if (entry) entries.push(entry);
  }
  for (const job of jobs) {
    const entry = localReviewEntry(job, headSha);
    if (entry) entries.push(entry);
  }
  return entries.sort((a, b) => Date.parse(a.at) - Date.parse(b.at) || a.key.localeCompare(b.key));
}

export function hasProviderReviews(events: ReadonlyArray<PREvent>): boolean {
  return events.some((event) => event.EventType === "issue_comment" && event.Body.includes(ROBOREV_PR_COMMENT_MARKER));
}

export function roborevVerdictLabel(entry: Pick<RoborevTimelineEntry, "verdict" | "findings">): string {
  switch (entry.verdict) {
    case "findings":
      if (entry.findings === null) return "Findings";
      return `${entry.findings} ${entry.findings === 1 ? "finding" : "findings"}`;
    case "clean":
      return "Clean";
    case "running":
      return "Running";
    case "failed":
      return "Failed";
    case "unknown":
      return "No verdict";
  }
}

export function roborevVerdictTone(entry: Pick<RoborevTimelineEntry, "verdict">): ChipTone {
  switch (entry.verdict) {
    case "findings":
      return "danger";
    case "clean":
      return "success";
    case "failed":
      return "warning";
    case "running":
    case "unknown":
      return "neutral";
  }
}
