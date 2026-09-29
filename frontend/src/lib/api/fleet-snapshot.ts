import { Cache, Context, Effect, Layer } from "effect";
import type { HostSummary as GeneratedHostSummary, Snapshot, WorkspaceSummary } from "./generated/models/index.js";

import type { ApiProblemError, TransientTransportError } from "./effect-errors.js";
import { executeGeneratedApiRequest } from "./generated-api.js";

export type HostSummary = GeneratedHostSummary;
export type FleetSnapshot = Snapshot;
export type FleetWorkspaceSummary = WorkspaceSummary;

const fetchFleetSnapshot = Effect.fn("FleetSnapshot.fetch")(function* () {
  return yield* executeGeneratedApiRequest("load fleet snapshot", (client, signal) =>
    client.FleetService.getSnapshot({ include_peers: true }, { signal }),
  );
});

export class FleetSnapshotReads extends Context.Service<
  FleetSnapshotReads,
  {
    readonly hosts: HostSummary[] | undefined;
    readonly load: Effect.Effect<FleetSnapshot, ApiProblemError | TransientTransportError>;
  }
>()("kenn-forge/FleetSnapshotReads") {}

export const FleetSnapshotReadsLive = Layer.effect(FleetSnapshotReads)(
  Effect.gen(function* () {
    // Keep directory rows for pickers without replaying stale workspace data.
    let hosts: HostSummary[] | undefined;
    // Share only pending reads. A later event or mutation refresh must read fresh authority.
    const pending = yield* Cache.make({
      capacity: 1,
      timeToLive: "0 millis",
      lookup: (_key: string) =>
        fetchFleetSnapshot().pipe(
          Effect.tap((snapshot) =>
            Effect.sync(() => {
              hosts = snapshot.hosts ?? [];
            }),
          ),
        ),
    });
    return {
      get hosts() {
        return hosts;
      },
      load: Cache.get(pending, "snapshot"),
    };
  }),
);

export const loadFleetSnapshot = Effect.fn("FleetSnapshot.load")(function* () {
  const reads = yield* FleetSnapshotReads;
  return yield* reads.load;
});

export const loadSnapshotHosts = Effect.fn("FleetSnapshot.loadHosts")(function* () {
  const data = yield* loadFleetSnapshot();
  return data.hosts ?? [];
});
