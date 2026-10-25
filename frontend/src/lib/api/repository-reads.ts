import { Cache, Context, Effect, Layer } from "effect";
import type { ApiProblemError, TransientTransportError } from "./effect-errors.js";
import { executeGeneratedApiRequest } from "./generated-api.js";
import type { RepoCatalog } from "./types.js";
import type { RepoResponse } from "./generated/models/repoResponse.js";
import { canonicalProvider, resolvedPlatformHost } from "./provider-routes.js";
import { repositoryKeyFromCatalog, repositoryKeyString } from "./repository-key.js";
import type { RepositoryRouteRef } from "../routes.js";
import { createRecentDetails } from "../stores/recent-details.js";

function detailSnapshotKey(ref: RepositoryRouteRef): string | undefined {
  if (!ref.repositoryKey) return undefined;
  return JSON.stringify([
    canonicalProvider(ref.provider),
    resolvedPlatformHost(ref.provider, ref.platformHost),
    repositoryKeyString(ref.repositoryKey),
    ref.repoPath,
  ]);
}

export class RepositoryReads extends Context.Service<
  RepositoryReads,
  {
    readonly snapshot: RepoCatalog[] | undefined;
    readonly refresh: Effect.Effect<RepoCatalog[], ApiProblemError | TransientTransportError>;
    // Presentation only: callers must revalidate before enabling repository actions.
    readonly detailSnapshot: (ref: RepositoryRouteRef) => RepoResponse | undefined;
    readonly rememberDetail: (repo: RepoResponse) => void;
  }
>()("kenn-forge/RepositoryReads") {}

export const RepositoryReadsLive = Layer.effect(RepositoryReads)(
  Effect.gen(function* () {
    let snapshot: RepoCatalog[] | undefined;
    const details = createRecentDetails<RepoResponse>(100);
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
      detailSnapshot: (ref) => {
        const key = detailSnapshotKey(ref);
        return key === undefined ? undefined : details.get(key);
      },
      rememberDetail: (repo) => {
        const key = detailSnapshotKey({
          provider: repo.Platform,
          platformHost: repo.PlatformHost,
          repositoryKey: repositoryKeyFromCatalog(repo),
          owner: repo.Owner,
          name: repo.Name,
          repoPath: `${repo.Owner}/${repo.Name}`,
        });
        if (key !== undefined) details.remember(key, repo);
      },
    };
  }),
);
