import { Effect, Stream } from "effect";
import { untrack } from "svelte";
import { getAppRuntime } from "../app/runtime-context.js";
import { RepositoryReads } from "../api/repository-reads.js";
import { canonicalProvider, resolvedPlatformHost, type ProviderRouteRef } from "../api/provider-routes.js";
import type { RepositoryRouteRef } from "../routes.js";

export function createRepositoryCatalog() {
  const runtime = getAppRuntime();
  let repositories = $state.raw<readonly RepositoryRouteRef[]>([]);

  $effect(() => {
    const execution = untrack(() =>
      runtime.runCommand(
        Effect.gen(function* () {
          const reads = yield* RepositoryReads;
          yield* Effect.all(
            [
              Stream.runForEach(reads.identities, (snapshot) =>
                Effect.sync(() => {
                  repositories = snapshot;
                }),
              ),
              // A failed refresh must not detach this view from later shared reads.
              reads.refresh.pipe(Effect.ignore),
            ],
            { concurrency: "unbounded", discard: true },
          );
        }),
        {
          operation: "load repository identities for detail selection",
          safeContext: {},
          onFailure: () => {},
        },
      ),
    );
    return execution.interrupt;
  });

  return {
    keyForRoute(ref: ProviderRouteRef) {
      const repo = repositories.find(
        (repo) =>
          canonicalProvider(repo.provider) === canonicalProvider(ref.provider) &&
          resolvedPlatformHost(repo.provider, repo.platformHost) ===
            resolvedPlatformHost(ref.provider, ref.platformHost) &&
          repo.owner === ref.owner &&
          repo.name === ref.name &&
          repo.repoPath === ref.repoPath,
      );
      return repo?.repositoryKey;
    },
  };
}
