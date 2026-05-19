// Typed helpers for the RFC 9457 problem+json envelope returned by every
// internal/server failure path. Frontend code should import ProblemCodes
// and isProblem from this module rather than substring-matching the
// human-readable detail text — code is the stable contract.

import type { components } from "./generated/schema";

export type ProblemBody = components["schemas"]["ProblemError"];

// ProblemCode is the closed union of wire codes emitted by the server.
// Drawn from the generated OpenAPI enum so a new server code lights up
// the union without manual sync.
export type ProblemCode = ProblemBody["code"];

// ProblemCodes is the runtime constant counterpart of ProblemCode for
// `switch` / `case ProblemCodes.unsupportedCapability:` style branching.
// Adding a code here is a typecheck failure if the wire enum drifts
// because the keys are typed against the union literal.
export const ProblemCodes = {
  badRequest: "badRequest",
  branchConflict: "branchConflict",
  commentNotFound: "commentNotFound",
  conflict: "conflict",
  forbidden: "forbidden",
  internalError: "internalError",
  issueNotFound: "issueNotFound",
  notFound: "notFound",
  payloadTooLarge: "payloadTooLarge",
  projectNotFound: "projectNotFound",
  pullNotFound: "pullNotFound",
  rateLimited: "rateLimited",
  repoNotFound: "repoNotFound",
  serviceUnavailable: "serviceUnavailable",
  settingsUnavailable: "settingsUnavailable",
  unauthorized: "unauthorized",
  unsupportedCapability: "unsupportedCapability",
  upstreamError: "upstreamError",
  validationError: "validationError",
  workspaceNotFound: "workspaceNotFound",
} as const satisfies Record<ProblemCode, ProblemCode>;

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
  return Object.prototype.hasOwnProperty.call(ProblemCodes, code);
}

// readProblem decodes a fetch Response body as a problem when the
// response is not OK and the content-type is application/problem+json.
// Returns null for ok responses, non-problem bodies, or parse failures.
export async function readProblem(
  response: Response,
): Promise<ProblemBody | null> {
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
