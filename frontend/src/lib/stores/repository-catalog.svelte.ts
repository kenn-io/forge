import { Effect } from "effect";
import { untrack } from "svelte";
import { getAppRuntime } from "../app/runtime-context.js";
import { RepositoryReads } from "../api/repository-reads.js";
import { canonicalProvider, resolvedPlatformHost, type ProviderRouteRef } from "../api/provider-routes.js";
import { repositoryKeyFromCatalog } from "../api/repository-key.js";
import type { RepoCatalog } from "../api/types.js";

export function createRepositoryCatalog() {
  const runtime = getAppRuntime();
  let repositories = $state.raw<readonly RepoCatalog[]>([]);

  $effect(() => {
    const execution = untrack(() =>
      runtime.runCommand(
        Effect.gen(function* () {
          const reads = yield* RepositoryReads;
          repositories = reads.snapshot ?? [];
          repositories = yield* reads.refresh;
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
          canonicalProvider(repo.Platform) === canonicalProvider(ref.provider) &&
          resolvedPlatformHost(repo.Platform, repo.PlatformHost) ===
            resolvedPlatformHost(ref.provider, ref.platformHost) &&
          repo.Owner === ref.owner &&
          repo.Name === ref.name &&
          `${repo.Owner}/${repo.Name}` === ref.repoPath,
      );
      return repositoryKeyFromCatalog(repo);
    },
  };
}
