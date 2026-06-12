/**
 * Helpers that collapse server-reported per-operation mutation
 * availability (`RepoOperations` from the detail payload, with the
 * /repo settings response as fallback) into the disabled/tooltip
 * pair the detail action controls consume.
 */

import type { OperationAvailability } from "../../api/types.js";

export type OperationGate = { unavailable: boolean; reason: string };

const availableGate: OperationGate = { unavailable: false, reason: "" };

/**
 * Gate for one operation. An absent entry (older server, detail
 * still loading) gates nothing — the capability checks around each
 * control still apply.
 */
export function operationGate(op: OperationAvailability | undefined): OperationGate {
  if (op === undefined || op.available) {
    return availableGate;
  }
  return { unavailable: true, reason: op.unavailable_reason ?? "" };
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
