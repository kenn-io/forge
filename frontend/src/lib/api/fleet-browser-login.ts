import { Effect } from "effect";
import { ApiProblemError, type TransientTransportError } from "./effect-errors.js";
import { executeGeneratedApiRequest } from "./generated-api.js";
import { ProblemCodes } from "./problems.js";
import { apiErrorMessage } from "./runtime.js";

export interface FleetBrowserLoginTarget {
  readonly nodeID: string;
  readonly baseURL: string;
}

// resolveFleetHostDestination returns where the machine switcher should send
// the browser: a one-time login link on the destination Forge, or its plain
// address when this Forge holds no direct credential for it (for example a
// sibling spoke reached through Tailscale Serve identity).
export const resolveFleetHostDestination = Effect.fn("FleetBrowserLogin.resolveDestination")(function* (
  host: FleetBrowserLoginTarget,
  path: string,
) {
  return yield* executeGeneratedApiRequest("sign in to fleet host", (client, signal) =>
    client.FleetService.createFleetBrowserLogin({ nodeId: host.nodeID }, { path }, { signal }),
  ).pipe(
    Effect.map((login) => login.url),
    Effect.catchIf(
      (failure): failure is ApiProblemError =>
        failure instanceof ApiProblemError && failure.problem.code === ProblemCodes.conflict,
      () => Effect.succeed(host.baseURL),
    ),
  );
});

export function fleetBrowserLoginFailureMessage(failure: ApiProblemError | TransientTransportError): string {
  if (failure instanceof ApiProblemError) {
    return apiErrorMessage(failure.problem, "Could not sign in to that Forge");
  }
  return "Could not start signing in to that Forge";
}
