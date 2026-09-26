import { assert, it } from "@effect/vitest";
import { Deferred, Effect, Fiber } from "effect";
import { makeAppLiveLayer } from "../app/layer.js";
import { makeGeneratedClient } from "../testing/generated-client.js";
import { makeGeneratedApiLayer } from "./generated-api.js";
import { loadFleetSnapshot, loadSnapshotHosts, type FleetSnapshot } from "./fleet-snapshot.js";

it("shares pending directory and workspace reads, then refreshes after completion", () =>
  Effect.runPromise(
    Effect.gen(function* () {
      const started = yield* Deferred.make<void>();
      const response = Promise.withResolvers<FleetSnapshot>();
      let calls = 0;
      let aborted = false;
      const client = makeGeneratedClient({
        FleetService: {
          getSnapshot: async (params, options) => {
            assert.deepStrictEqual(params, { include_peers: true });
            calls++;
            options?.signal?.addEventListener("abort", () => {
              aborted = true;
            });
            Deferred.doneUnsafe(started, Effect.void);
            return response.promise;
          },
        },
      });
      yield* Effect.gen(function* () {
        const directory = yield* Effect.forkChild(loadSnapshotHosts());
        yield* Deferred.await(started);
        const workspaces = yield* Effect.forkChild(loadFleetSnapshot());
        yield* Effect.yieldNow;
        assert.strictEqual(calls, 1);

        // Closing one consumer must not abort the other consumer's pending read.
        yield* Fiber.interrupt(directory);
        assert.strictEqual(aborted, false);
        response.resolve({
          generation: 7,
          protocolVersion: 3,
          hosts: [],
          projects: [],
          sessions: [],
          workspaces: [],
          worktrees: [],
        });
        assert.strictEqual((yield* Fiber.join(workspaces)).generation, 7);
        yield* loadFleetSnapshot();
        assert.strictEqual(calls, 2);
      }).pipe(Effect.provide(makeAppLiveLayer(makeGeneratedApiLayer(client))));
    }),
  ));
