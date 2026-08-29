import { Effect } from "effect";
import type { components } from "./generated/schema.js";

import { executeGeneratedApiRequest } from "./generated-api.js";

export type HostSummary = components["schemas"]["HostSummary"];
export type FleetSnapshot = components["schemas"]["Snapshot"];
export type FleetWorkspaceSummary = components["schemas"]["WorkspaceSummary"];

export const loadFleetSnapshot = Effect.fn("FleetSnapshot.load")(function* () {
  return yield* executeGeneratedApiRequest("load fleet snapshot", (client, signal) =>
    client.GET("/snapshot", {
      params: { query: { include_peers: true } },
      signal,
    }),
  );
});

export const loadSnapshotHosts = Effect.fn("FleetSnapshot.loadHosts")(function* () {
  const data = yield* loadFleetSnapshot();
  return data.hosts ?? [];
});
