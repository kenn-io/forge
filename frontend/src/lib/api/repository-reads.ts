import { Cache, Context, Effect, Layer } from "effect";
import type { ApiProblemError, TransientTransportError } from "./effect-errors.js";
import { executeGeneratedApiRequest } from "./generated-api.js";
import type { RepoCatalog } from "./types.js";

export class RepositoryReads extends Context.Service<
  RepositoryReads,
  {
    readonly snapshot: RepoCatalog[] | undefined;
    readonly refresh: Effect.Effect<RepoCatalog[], ApiProblemError | TransientTransportError>;
  }
>()("kenn-forge/RepositoryReads") {}

export const RepositoryReadsLive = Layer.effect(RepositoryReads)(
  Effect.gen(function* () {
    let snapshot: RepoCatalog[] | undefined;
    const pending = yield* Cache.make({
      capacity: 1,
      timeToLive: "0 millis",
      lookup: (_key: string) =>
        executeGeneratedApiRequest("load repositories", (client, signal) =>
          client.RepositoriesService.listRepos({ signal }),
        ).pipe(
          Effect.tap((repos) =>
            Effect.sync(() => {
              snapshot = repos;
            }),
          ),
        ),
    });
    return {
      get snapshot() {
        return snapshot;
      },
      refresh: Cache.get(pending, "repositories"),
    };
  }),
);
