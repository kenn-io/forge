/**
 * Helpers that collapse server-reported per-operation mutation
 * availability (`RepoOperations` from the detail payload, with the
 * /repo settings response as fallback) into the disabled/tooltip
 * pair the detail action controls consume.
 */

import type { OperationAvailability, RepoOperations } from "../../api/types.js";

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

const writeCredentialCodes = new Set(["missing_write_credential", "write_credential_error"]);

/**
 * Host-wide write-credential gate. The two write-credential codes are
 * host-scoped by construction — the mutation chain either resolves a
 * user credential for the host or it does not — so provider writes
 * without a dedicated operation key (title/body/task-list edits,
 * comment edits, review-thread replies and resolution) gate on any
 * operation carrying one of these codes. Operation-specific states
 * like rate limits stay per-operation and do not trip this gate.
 */
export function hostWriteCredentialGate(ops: RepoOperations | undefined): OperationGate {
  if (ops === undefined) {
    return availableGate;
  }
  for (const op of Object.values(ops) as (OperationAvailability | undefined)[]) {
    if (op !== undefined && !op.available && op.code !== undefined && writeCredentialCodes.has(op.code)) {
      return { unavailable: true, reason: op.unavailable_reason ?? "" };
    }
  }
  return availableGate;
}
