import { Context, Effect, Layer } from "effect";
import { makeOrderedCommandQueue, type CommandQueueClosed } from "../effect/ordered-command-queue.js";
import type { NumberedRouteItemRef } from "../routes.js";
import type { RoutableItemRef } from "../stores/router.svelte.js";
import { invokeMutationCallback } from "../stores/ordered-mutations.js";
import type { ResolvableItemReference } from "../utils/item-reference.js";
import { resolveItemReferenceEffect } from "../utils/itemRefHandler.js";
import { InvalidExternalPayload, type ApiProblemError, type TransientTransportError } from "./effect-errors.js";
import { executeGeneratedApiRequest } from "./generated-api.js";
import type { WorkspaceTargetSelection } from "./generated/models/workspaceTargetSelection.js";
import { canonicalProvider, resolvedPlatformHost } from "./provider-routes.js";
import { repositoryKeyFromCatalog, repositoryKeyToRequiredWire } from "./repository-key.js";
import { RepositoryReads } from "./repository-reads.js";

interface WorkspaceTargetUpdate {
  readonly id: string;
  readonly hostKey: string | undefined;
  readonly type: "pr" | "issue";
  readonly item: NumberedRouteItemRef;
  readonly hidden: boolean;
}

interface WorkspaceTargetReferenceVisit {
  readonly id: string;
  readonly hostKey: string | undefined;
  readonly reference: ResolvableItemReference;
  readonly onResolved: (item: RoutableItemRef) => void;
}

type WorkspaceTargetCommand = WorkspaceTargetUpdate | WorkspaceTargetReferenceVisit;

const persistWorkspaceTarget = Effect.fn("persistWorkspaceTarget")(function* ({
  id,
  hostKey,
  type,
  item,
  hidden,
}: WorkspaceTargetUpdate) {
  const provider = canonicalProvider(item.provider);
  const platformHost = resolvedPlatformHost(item.provider, item.platformHost).toLowerCase();
  let key = item.repositoryKey;
  if (!key) {
    const reads = yield* RepositoryReads;
    const repos = yield* reads.refresh;
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

export class WorkspaceTargetMutations extends Context.Service<
  WorkspaceTargetMutations,
  {
    readonly submit: (
      input: WorkspaceTargetCommand,
    ) => Effect.Effect<void, ApiProblemError | TransientTransportError | InvalidExternalPayload | CommandQueueClosed>;
  }
>()("kenn-forge/WorkspaceTargetMutations") {}

export const WorkspaceTargetMutationsLive = Layer.effect(WorkspaceTargetMutations)(
  makeOrderedCommandQueue(
    "workspace target mutations",
    Effect.fn("executeWorkspaceTargetCommand")(function* (command: WorkspaceTargetCommand) {
      if (!("reference" in command)) return yield* persistWorkspaceTarget(command);
      const item = yield* resolveItemReferenceEffect(command.reference);
      if (!item) return;
      yield* invokeMutationCallback(() => command.onResolved(item));
      yield* persistWorkspaceTarget({
        id: command.id,
        hostKey: command.hostKey,
        type: item.itemType,
        item,
        hidden: false,
      });
    }),
  ),
);

export const visitWorkspaceTargetReference = Effect.fn("visitWorkspaceTargetReference")(function* (
  id: string,
  hostKey: string | undefined,
  reference: ResolvableItemReference,
  onResolved: (item: RoutableItemRef) => void,
) {
  const mutations = yield* WorkspaceTargetMutations;
  yield* mutations.submit({ id, hostKey, reference, onResolved });
});

export const updateWorkspaceTarget = Effect.fn("updateWorkspaceTarget")(function* (
  id: string,
  hostKey: string | undefined,
  type: "pr" | "issue",
  item: NumberedRouteItemRef,
  hidden: boolean,
) {
  const mutations = yield* WorkspaceTargetMutations;
  yield* mutations.submit({ id, hostKey, type, item, hidden });
});
