import { Effect } from "effect";
import { SvelteMap, SvelteSet } from "svelte/reactivity";
import type { RepoOperations } from "../api/types.js";
import type { RepoRefResponse } from "../api/generated/models/index.js";
import type { AppRuntime } from "../app/runtime.js";
import { executeGeneratedApiRequest } from "../api/generated-api.js";
import { retryIdempotentRead } from "../api/retry-policy.js";
import { providerHostRouteParams, providerRouteParams, providerUsesHostRoute } from "../api/provider-routes.js";

// List rows carry provider capabilities but not mutation availability
// (write credentials, rate limits, viewer permission), which only detail and
// repository reads report. Sidebar menus read it from here, one request per
// repository, so a blocked action is disabled with its reason instead of
// failing after the click. An absent entry means "no verdict yet": callers
// fall back to capability gating and the server stays authoritative.
// The same read carries the merge methods the repository allows.
const operationsByRepo = new SvelteMap<string, RepoOperations>();
const mergeMethodsByRepo = new SvelteMap<string, MergeMethod[]>();
// Repositories whose latest read failed after its retries.
const failedRepos = new SvelteSet<string>();
const inFlight = new Set<string>();

type RepoIdentity = Pick<RepoRefResponse, "provider" | "platform_host" | "owner" | "name" | "repo_path">;

function repoKey(repo: RepoIdentity): string {
  return `${repo.provider}\0${repo.platform_host}\0${repo.owner}/${repo.name}`;
}

export function repoOperations(repo: RepoIdentity): RepoOperations | undefined {
  return operationsByRepo.get(repoKey(repo));
}

/** Whether the latest read of the repository failed; a new refresh clears it. */
export function repoReadFailed(repo: RepoIdentity): boolean {
  return failedRepos.has(repoKey(repo));
}

export type MergeMethod = "squash" | "merge" | "rebase";

/** The merge methods the repository allows, in the merge dialog's order. */
export function repoMergeMethods(repo: RepoIdentity): readonly MergeMethod[] | undefined {
  return mergeMethodsByRepo.get(repoKey(repo));
}

/** Refreshes availability for each repository; a failed read keeps the last verdict. */
export function refreshRepoOperations(runtime: AppRuntime, repos: readonly RepoIdentity[]): void {
  for (const repo of repos) {
    const key = repoKey(repo);
    if (inFlight.has(key)) continue;
    inFlight.add(key);
    failedRepos.delete(key);
    const ref = {
      provider: repo.provider,
      platformHost: repo.platform_host,
      owner: repo.owner,
      name: repo.name,
      repoPath: repo.repo_path,
    };
    runtime.runCommand(
      executeGeneratedApiRequest("GET repository operations", (client, signal) =>
        providerUsesHostRoute(ref)
          ? client.RepositoriesService.getRepoOnHost(providerHostRouteParams(ref), { signal })
          : client.RepositoriesService.getRepo(providerRouteParams(ref), { signal }),
      ).pipe(
        retryIdempotentRead,
        Effect.tap((response) =>
          Effect.sync(() => {
            operationsByRepo.set(key, response.operations);
            mergeMethodsByRepo.set(key, [
              ...(response.AllowSquashMerge ? (["squash"] as const) : []),
              ...(response.AllowMergeCommit ? (["merge"] as const) : []),
              ...(response.AllowRebaseMerge ? (["rebase"] as const) : []),
            ]);
          }),
        ),
        Effect.ensuring(Effect.sync(() => inFlight.delete(key))),
      ),
      {
        operation: "load repository operations",
        safeContext: { owner: repo.owner, name: repo.name },
        onFailure: () => {
          failedRepos.add(key);
        },
      },
    );
  }
}
