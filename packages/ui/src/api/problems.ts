// Typed helpers for the RFC 9457 problem+json envelope returned by every
// internal/server failure path. Frontend code should import ProblemCodes
// and isProblem from this module rather than substring-matching the
// human-readable detail text — code is the stable contract.

import { problemErrorCodeValues } from "./generated/schema";
import type { components } from "./generated/schema";

export type ProblemBody = components["schemas"]["ProblemError"];

// ProblemCode is the closed union of wire codes emitted by the server.
// Drawn from the generated OpenAPI enum so a new server code lights up
// the union without manual sync.
export type ProblemCode = ProblemBody["code"];

export const ProblemCodes = Object.fromEntries(problemErrorCodeValues.map((code) => [code, code])) as {
  readonly [K in ProblemCode]: K;
};

const problemCodeValues = new Set<string>(problemErrorCodeValues);

// isProblem narrows an unknown value (e.g. a parsed JSON response body)
// to ProblemBody. It checks the top-level shape - object with a code
// field that matches one of the known codes.
export function isProblem(value: unknown): value is ProblemBody {
  if (!value || typeof value !== "object") {
    return false;
  }
  const code = (value as { code?: unknown }).code;
  if (typeof code !== "string") {
    return false;
  }
  return problemCodeValues.has(code);
}

// readProblem decodes a fetch Response body as a problem when the
// response is not OK and the content-type is application/problem+json.
// Returns null for ok responses, non-problem bodies, or parse failures.
export async function readProblem(response: Response): Promise<ProblemBody | null> {
  if (response.ok) {
    return null;
  }
  const ct = response.headers.get("content-type") ?? "";
  if (!ct.includes("application/problem+json") && !ct.includes("application/json")) {
    return null;
  }
  let body: unknown;
  try {
    body = await response.json();
  } catch {
    return null;
  }
  return isProblem(body) ? body : null;
}

// problemCapability reads details.capability from an unsupportedCapability
// problem so call sites can render a typed tooltip without dipping into
// the loose details record.
export function problemCapability(problem: ProblemBody): string | undefined {
  if (problem.code !== ProblemCodes.unsupportedCapability) {
    return undefined;
  }
  const cap = problem.details?.["capability"];
  return typeof cap === "string" ? cap : undefined;
}

// ConflictReason is the details.reason subtype carried by conflict
// problems on head-bound mutations (merge, approve), per the head
// binding contract in context/provider-architecture.md.
export type ConflictReason = "stale_state" | "head_unknown" | "conflict";

// problemConflictReason reads details.reason from a conflict problem.
// Non-conflict problems return undefined; a conflict with a missing or
// unrecognized reason collapses to the generic "conflict" so callers
// can branch on a closed union.
export function problemConflictReason(problem: ProblemBody): ConflictReason | undefined {
  if (problem.code !== ProblemCodes.conflict) {
    return undefined;
  }
  const reason = problem.details?.["reason"];
  if (reason === "stale_state" || reason === "head_unknown") {
    return reason;
  }
  return "conflict";
}

// problemConflictContext reads details.context from a conflict
// problem: provider side-effect context that must reach the user — an
// approval that could not be revoked, posted review text a retry would
// repeat — per the stale-approval contract in context/error-handling.md.
export function problemConflictContext(problem: ProblemBody): string | undefined {
  if (problem.code !== ProblemCodes.conflict) {
    return undefined;
  }
  const context = problem.details?.["context"];
  return typeof context === "string" && context !== "" ? context : undefined;
}

// problemRetryAfter reads details.retryAfter from a rateLimited problem
// and returns it parsed as a Date. Returns undefined when the field is
// missing or not a valid RFC 3339 string.
export function problemRetryAfter(problem: ProblemBody): Date | undefined {
  if (problem.code !== ProblemCodes.rateLimited) {
    return undefined;
  }
  const retry = problem.details?.["retryAfter"];
  if (typeof retry !== "string") {
    return undefined;
  }
  const parsed = new Date(retry);
  return Number.isNaN(parsed.getTime()) ? undefined : parsed;
}
