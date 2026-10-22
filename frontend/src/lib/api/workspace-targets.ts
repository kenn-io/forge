import { Context, Effect, Layer } from "effect";
import { makeOrderedCommandQueue, type CommandQueueClosed } from "../effect/ordered-command-queue.js";
import type { NumberedRouteItemRef } from "../routes.js";
import { invokeMutationCallback } from "../stores/ordered-mutations.js";
import type { ResolvableItemReference } from "../utils/item-reference.js";
import { resolveItemReferenceEffect, type ItemReferenceResolution } from "../utils/itemRefHandler.js";
import { InvalidExternalPayload, type ApiProblemError, type TransientTransportError } from "./effect-errors.js";
import { executeGeneratedApiRequest } from "./generated-api.js";
import type { WorkspaceTargetSelection } from "./generated/models/workspaceTargetSelection.js";
import {
  canonicalProvider,
  resolvedPlatformHost,
  providerUsesHostRoute,
  providerHostRouteParams,
  providerRouteParams,
} from "./provider-routes.js";
import { repositoryKeyFromCatalog, repositoryKeyToRequiredWire } from "./repository-key.js";

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
  readonly onResolution: (resolution: ItemReferenceResolution) => void;
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
    const ref = { ...item, provider, platformHost };
    const repo = yield* executeGeneratedApiRequest("resolve target repository", (client, signal) =>
      providerUsesHostRoute(ref)
        ? client.RepositoriesService.getRepoOnHost(providerHostRouteParams(ref), { signal })
        : client.RepositoriesService.getRepo(providerRouteParams(ref), { signal }),
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
      const resolution = yield* resolveItemReferenceEffect(command.reference);
      yield* invokeMutationCallback(() => command.onResolution(resolution));
      if (resolution.kind !== "item") return;
      const { item } = resolution;
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
  onResolution: (resolution: ItemReferenceResolution) => void,
) {
  const mutations = yield* WorkspaceTargetMutations;
  yield* mutations.submit({ id, hostKey, reference, onResolution });
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
