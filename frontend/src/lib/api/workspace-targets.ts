import { Effect } from "effect";
import type { NumberedRouteItemRef } from "../routes.js";
import { InvalidExternalPayload } from "./effect-errors.js";
import { executeGeneratedApiRequest } from "./generated-api.js";
import type { WorkspaceTargetSelection } from "./generated/models/workspaceTargetSelection.js";
import { canonicalProvider, resolvedPlatformHost } from "./provider-routes.js";
import { repositoryKeyFromCatalog, repositoryKeyToRequiredWire } from "./repository-key.js";
import { RepositoryReads } from "./repository-reads.js";

export const updateWorkspaceTarget = Effect.fn("updateWorkspaceTarget")(function* (
  id: string,
  hostKey: string | undefined,
  type: "pr" | "issue",
  item: NumberedRouteItemRef,
  hidden: boolean,
) {
  const provider = canonicalProvider(item.provider);
  const platformHost = resolvedPlatformHost(item.provider, item.platformHost).toLowerCase();
  let key = item.repositoryKey;
  if (!key) {
    const reads = yield* RepositoryReads;
    const repos = reads.snapshot ?? (yield* reads.refresh);
    const repo = repos.find(
      (candidate) =>
        canonicalProvider(candidate.Platform) === provider &&
        resolvedPlatformHost(candidate.Platform, candidate.PlatformHost).toLowerCase() === platformHost &&
        candidate.Owner.toLowerCase() === item.owner.toLowerCase() &&
        candidate.Name.toLowerCase() === item.name.toLowerCase(),
    );
    key = repositoryKeyFromCatalog(repo);
  }
  if (!key) {
    return yield* InvalidExternalPayload.make({
      operation: "resolve target repository",
      cause: "Repository identity is unavailable",
    });
  }
  const body = {
    repository: { provider, platform_host: platformHost, ...repositoryKeyToRequiredWire(key) },
    type,
    number: item.number,
    hidden,
  } satisfies WorkspaceTargetSelection;
  yield* executeGeneratedApiRequest<unknown>("save workspace target", (client, signal) => {
    if (hostKey?.startsWith("devbox:")) {
      return client.DevboxesService.updateDevboxWorkspaceTarget({ connectionId: hostKey.slice(7), id }, body, {
        signal,
      });
    }
    if (hostKey) return client.FleetService.updateFleetWorkspaceTarget({ hostKey, id }, body, { signal });
    return client.WorkspacesService.updateWorkspaceTarget({ id }, body, { signal });
  });
});
