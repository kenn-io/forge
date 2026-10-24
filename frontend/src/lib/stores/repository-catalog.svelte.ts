import { Effect, Stream } from "effect";
import { untrack } from "svelte";
import { getAppRuntime } from "../app/runtime-context.js";
import { RepositoryReads, type RepositoryIdentity } from "../api/repository-reads.js";
import { canonicalProvider, resolvedPlatformHost, type ProviderRouteRef } from "../api/provider-routes.js";
import type { RepositoryKey } from "../api/repository-key.js";

export function createRepositoryCatalog() {
  const runtime = getAppRuntime();
  let repositories = $state.raw<readonly RepositoryIdentity[]>([]);

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
              // Navigation reuses the shared catalog; events reconcile it separately.
              // A failed first read must not detach the view from later shared reads.
              reads.snapshot === undefined ? reads.refresh.pipe(Effect.ignore) : Effect.void,
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
    keyForRoute(ref: ProviderRouteRef, rowKey?: RepositoryKey) {
      const repo = repositories.find(
        (repo) =>
          canonicalProvider(repo.provider) === canonicalProvider(ref.provider) &&
          resolvedPlatformHost(repo.provider, repo.platformHost) ===
            resolvedPlatformHost(ref.provider, ref.platformHost) &&
          repo.owner === ref.owner &&
          repo.name === ref.name &&
          repo.repoPath === ref.repoPath,
      );
      return (repo?.fromDetail ? repo.repositoryKey : undefined) ?? rowKey ?? repo?.repositoryKey;
    },
  };
}
