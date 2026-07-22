/**
 * Helpers that collapse server-reported per-operation mutation
 * availability (`RepoOperations` from the detail payload, with the
 * /repo settings response as fallback) into the disabled/tooltip
 * pair the detail action controls consume.
 */

import type { OperationAvailability } from "../../api/types.js";

export type OperationGate = { unavailable: boolean; reason: string };

const availableGate: OperationGate = { unavailable: false, reason: "" };

function unavailableReason(op: OperationAvailability): string {
  const reason = op.unavailable_reason ?? "";
  if (op.code !== "rate_limited" || !op.retry_at) return reason;

  const retryAt = new Date(op.retry_at);
  if (Number.isNaN(retryAt.getTime())) return reason;

  const localRetryTime = retryAt.toLocaleTimeString(undefined, {
    hour: "2-digit",
    minute: "2-digit",
    hourCycle: "h23",
  });
  return reason ? `${reason}; retry at ${localRetryTime}` : `Retry at ${localRetryTime}`;
}

/**
 * Gate for one operation.
 *
 * Contract for absent entries (mirrored on the server's
 * RepoOperations doc): the current server always emits every field,
 * so `undefined` only means an older server or a payload that has
 * not loaded yet. That is "no operation-level verdict", NOT
 * "unavailable" — the control falls back to its capability gating,
 * exactly the pre-operations behavior. Treating absence as disabled
 * would blank every action against older servers and flash-disable
 * the UI on each route change.
 */
export function operationGate(op: OperationAvailability | undefined): OperationGate {
  if (op === undefined || op.available) {
    return availableGate;
  }
  return { unavailable: true, reason: unavailableReason(op) };
}

/**
 * Gate for a control backed by several operations (e.g. the label
 * picker adds and removes): the first unavailable one decides.
 */
export function firstUnavailableGate(...ops: (OperationAvailability | undefined)[]): OperationGate {
  for (const op of ops) {
    const gate = operationGate(op);
    if (gate.unavailable) return gate;
  }
  return availableGate;
}
