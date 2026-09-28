import { Context, Effect, Layer } from "effect";
import { ApiProblemError, type TransientTransportError } from "../api/effect-errors.js";
import { GeneratedApi } from "../api/generated-api.js";
import type { ExternalContextCard } from "../api/generated/models/externalContextCard.js";
import type { ExternalContextSourceInfo } from "../api/generated/models/externalContextSourceInfo.js";
import {
  canonicalProvider,
  providerHostRouteParams,
  providerRouteParams,
  providerUsesHostRoute,
  resolvedPlatformHost,
  type ProviderRouteRef,
} from "../api/provider-routes.js";
import { apiErrorMessage } from "../api/runtime.js";

export interface ExternalContextPull {
  readonly ref: ProviderRouteRef;
  readonly platformRepoId: string;
  readonly number: number;
  readonly headSha: string;
}

export function externalContextKey(pull: ExternalContextPull): string {
  const { ref } = pull;
  return JSON.stringify([
    canonicalProvider(ref.provider),
    resolvedPlatformHost(ref.provider, ref.platformHost),
    pull.platformRepoId,
    ref.repoPath,
    ref.owner,
    ref.name,
    pull.number,
    pull.headSha,
  ]);
}

export class ExternalContextState {
  card = $state.raw<ExternalContextCard | null>(null);
  error = $state<string | null>(null);
  loading = $state(false);
  pendingAction = $state<string | null>(null);
  needsRefresh = $state(false);
  generation = 0;
}

type ExternalContextError = ApiProblemError | TransientTransportError;

export function externalContextErrorMessage(error: ExternalContextError): string {
  return error instanceof ApiProblemError
    ? apiErrorMessage(error.problem, "Unable to load external context.")
    : "Unable to reach external context. Refresh to try again.";
}

export interface ExternalContextWorkflowService {
  readonly revision: number;
  readonly invalidate: Effect.Effect<void>;
  readonly sources: Effect.Effect<ExternalContextSourceInfo[], ExternalContextError>;
  readonly state: (pull: ExternalContextPull, sourceId: string) => ExternalContextState;
  readonly read: (pull: ExternalContextPull, sourceId: string, refresh?: boolean) => Effect.Effect<void>;
  readonly action: (pull: ExternalContextPull, sourceId: string, actionId: string) => Effect.Effect<void>;
}

export class ExternalContextWorkflow extends Context.Service<ExternalContextWorkflow, ExternalContextWorkflowService>()(
  "kenn-forge/ExternalContextWorkflow",
) {}

export const ExternalContextWorkflowLive = Layer.effect(ExternalContextWorkflow)(
  Effect.gen(function* () {
    const api = yield* GeneratedApi;
    const entries = new Map<string, ExternalContextState>();
    let revision = $state(0);

    function state(pull: ExternalContextPull, sourceId: string): ExternalContextState {
      const key = JSON.stringify([externalContextKey(pull), sourceId]);
      let entry = entries.get(key);
      if (!entry) {
        entry = new ExternalContextState();
        entries.set(key, entry);
      }
      return entry;
    }

    const read = Effect.fn("ExternalContext.read")(function* (
      pull: ExternalContextPull,
      sourceId: string,
      refresh = false,
    ) {
      const entry = state(pull, sourceId);
      if (entry.pendingAction !== null) return;
      const generation = ++entry.generation;
      entry.loading = true;
      const query = { platform_repo_id: pull.platformRepoId, refresh };
      yield* api
        .execute("load external context", (signal) =>
          providerUsesHostRoute(pull.ref)
            ? api.client.ExternalContextService.getPullExternalContextOnHost(
                { ...providerHostRouteParams(pull.ref), number: pull.number, sourceId },
                query,
                { signal },
              )
            : api.client.ExternalContextService.getPullExternalContext(
                { ...providerRouteParams(pull.ref), number: pull.number, sourceId },
                query,
                { signal },
              ),
        )
        .pipe(
          Effect.tap((result) =>
            Effect.sync(() => {
              if (entry.generation !== generation) return;
              entry.card = result.card ?? null;
              entry.error = null;
              entry.needsRefresh = false;
            }),
          ),
          Effect.catch((error) =>
            Effect.sync(() => {
              if (entry.generation === generation) entry.error = externalContextErrorMessage(error);
            }),
          ),
          Effect.ensuring(
            Effect.sync(() => {
              if (entry.generation === generation) entry.loading = false;
            }),
          ),
        );
    });

    const action = Effect.fn("ExternalContext.action")(function* (
      pull: ExternalContextPull,
      sourceId: string,
      actionId: string,
    ) {
      const entry = state(pull, sourceId);
      if (entry.pendingAction !== null || entry.needsRefresh || !pull.headSha) return;
      const generation = ++entry.generation;
      entry.loading = false;
      entry.pendingAction = actionId;
      entry.error = null;
      const body = { platform_repo_id: pull.platformRepoId, head_sha: pull.headSha };
      yield* api
        .execute("run external context action", (signal) =>
          providerUsesHostRoute(pull.ref)
            ? api.client.ExternalContextService.runPullExternalContextActionOnHost(
                { ...providerHostRouteParams(pull.ref), number: pull.number, sourceId, actionId },
                body,
                { signal },
              )
            : api.client.ExternalContextService.runPullExternalContextAction(
                { ...providerRouteParams(pull.ref), number: pull.number, sourceId, actionId },
                body,
                { signal },
              ),
        )
        .pipe(
          Effect.tap((result) =>
            Effect.sync(() => {
              if (entry.generation === generation) entry.card = result.card ?? null;
            }),
          ),
          Effect.catch((error) =>
            Effect.sync(() => {
              entry.error = externalContextErrorMessage(error);
              if (!(error instanceof ApiProblemError))
                entry.error += " The action may have been submitted. Refresh to check its status.";
              entry.needsRefresh = true;
            }),
          ),
          Effect.ensuring(
            Effect.sync(() => {
              const invalidated = entry.generation !== generation;
              ++entry.generation;
              entry.pendingAction = null;
              if (invalidated) revision++;
            }),
          ),
        );
    });

    return {
      get revision() {
        return revision;
      },
      invalidate: Effect.sync(() => {
        revision++;
        for (const entry of entries.values()) ++entry.generation;
      }),
      sources: api
        .execute("list external context sources", (signal) =>
          api.client.ExternalContextService.listExternalContextSources({ signal }),
        )
        .pipe(Effect.map((result) => result.sources ?? [])),
      state,
      read,
      action,
    };
  }),
);
