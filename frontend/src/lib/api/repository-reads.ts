import { Cache, Context, Effect, Layer, Stream, SubscriptionRef } from "effect";
import type { ApiProblemError, TransientTransportError } from "./effect-errors.js";
import { executeGeneratedApiRequest } from "./generated-api.js";
import type { RepoCatalog } from "./types.js";
import type { RepoResponse } from "./generated/models/repoResponse.js";
import { canonicalProvider, resolvedPlatformHost } from "./provider-routes.js";
import { repositoryKeyFromCatalog, repositoryKeyString, sameRepositoryKey } from "./repository-key.js";
import type { RepositoryRouteRef } from "../routes.js";
import { createRecentDetails } from "../stores/recent-details.js";

export interface RepositoryIdentity extends RepositoryRouteRef {
  readonly fromDetail: boolean;
}

function routeKey(ref: RepositoryRouteRef): string {
  return JSON.stringify([
    canonicalProvider(ref.provider),
    resolvedPlatformHost(ref.provider, ref.platformHost),
    ref.repoPath,
  ]);
}

function sameRoute(a: RepositoryRouteRef, b: RepositoryRouteRef): boolean {
  return routeKey(a) === routeKey(b);
}

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
    readonly identities: Stream.Stream<readonly RepositoryIdentity[]>;
    readonly observeRoute: (ref: RepositoryRouteRef) => Effect.Effect<void>;
    readonly refresh: Effect.Effect<RepoCatalog[], ApiProblemError | TransientTransportError>;
    // Presentation only: callers must revalidate before enabling repository actions.
    readonly detailSnapshot: (ref: RepositoryRouteRef) => RepoResponse | undefined;
    readonly rememberDetail: (repo: RepoResponse) => void;
  }
>()("kenn-forge/RepositoryReads") {}

export const RepositoryReadsLive = Layer.effect(RepositoryReads)(
  Effect.gen(function* () {
    let snapshot: RepoCatalog[] | undefined;
    let observationRevision = 0;
    const routeObservationRevisions = new Map<string, number>();
    const identities = yield* SubscriptionRef.make<readonly RepositoryIdentity[]>([]);
    const details = createRecentDetails<RepoResponse>(100);
    const pending = yield* Cache.make({
      capacity: 1,
      timeToLive: "0 millis",
      lookup: (_key: string) =>
        Effect.gen(function* () {
          const revision = observationRevision;
          const repos = yield* executeGeneratedApiRequest("load repositories", (client, signal) =>
            client.RepositoriesService.listRepos({ signal }),
          );
          snapshot = repos;
          const catalog = repos.map((repo) => ({
            provider: repo.Platform,
            platformHost: repo.PlatformHost,
            repositoryKey: repositoryKeyFromCatalog(repo),
            owner: repo.Owner,
            name: repo.Name,
            repoPath: `${repo.Owner}/${repo.Name}`,
            fromDetail: false,
          }));
          yield* SubscriptionRef.update(identities, (current) => [
            ...catalog.map(
              (repo) =>
                current.find(
                  (prior) =>
                    prior.fromDetail &&
                    sameRoute(prior, repo) &&
                    // Protect only routes observed after this catalog request began.
                    ((routeObservationRevisions.get(routeKey(repo)) ?? 0) > revision ||
                      sameRepositoryKey(prior.repositoryKey, repo.repositoryKey)),
                ) ?? repo,
            ),
            // The catalog lists tracked repositories; details can still exist outside it.
            ...current.filter((prior) => prior.fromDetail && !catalog.some((repo) => sameRoute(prior, repo))),
          ]);
          return repos;
        }),
    });
    return {
      get snapshot() {
        return snapshot;
      },
      identities: SubscriptionRef.changes(identities),
      observeRoute: Effect.fn("observe repository route identity")(function* (ref: RepositoryRouteRef) {
        if (ref.repositoryKey === undefined) return;
        routeObservationRevisions.set(routeKey(ref), ++observationRevision);
        yield* SubscriptionRef.update(identities, (current) => [
          ...current.filter((repo) => !sameRoute(repo, ref)),
          { ...ref, fromDetail: true },
        ]);
      }),
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

export const refreshRepositoryCatalog = Effect.gen(function* () {
  return yield* (yield* RepositoryReads).refresh;
});

export const observeRepositoryRoute = Effect.fn("observe repository route")(function* (ref: RepositoryRouteRef) {
  yield* (yield* RepositoryReads).observeRoute(ref);
});
