import { Effect } from "effect";
import type { components } from "./generated/schema.js";

import { executeGeneratedApiRequest } from "./generated-api.js";

export type HostSummary = components["schemas"]["HostSummary"];

export const loadSnapshotHosts = Effect.fn("FleetSnapshot.loadHosts")(function* () {
  const data = yield* executeGeneratedApiRequest("load fleet snapshot hosts", (client, signal) =>
    client.GET("/snapshot", {
      params: { query: { include_peers: true } },
      signal,
    }),
  );
  return data.hosts ?? [];
});
