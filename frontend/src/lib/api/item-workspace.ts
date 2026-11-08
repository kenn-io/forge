import { Effect } from "effect";
import { executeGeneratedApiRequest, executeOpaqueGeneratedApiRequest } from "./generated-api.js";
import {
  providerHostRouteParams,
  providerRouteParams,
  providerUsesHostRoute,
  resolvedPlatformHost,
  type ProviderRouteRef,
} from "./provider-routes.js";
import { repositoryKeyToWire } from "./repository-key.js";

export type ItemWorkspaceRequest = {
  readonly kind: "pull" | "issue";
  readonly repo: ProviderRouteRef;
  readonly number: number;
  readonly gitHeadRef?: string | undefined;
  readonly reuseExistingBranch?: boolean | undefined;
  readonly reuseExistingDirectory?: boolean | undefined;
};

export type CreatedItemWorkspace = { readonly id?: string; readonly status?: string };

function created(value: unknown): CreatedItemWorkspace {
  if (typeof value !== "object" || value === null) return {};
  const record = value as Record<string, unknown>;
  return {
    ...(typeof record.id === "string" ? { id: record.id } : {}),
    ...(typeof record.status === "string" ? { status: record.status } : {}),
  };
}

/**
 * Creates the workspace for a pull request or issue on one machine: this
 * Forge when hostKey is unset, a devbox (devbox:<id>), or a fleet member by
 * node ID. A spoke sends a fleet member's request through its hub.
 */
export function createItemWorkspace(hostKey: string | undefined, request: ItemWorkspaceRequest) {
  const { kind, repo, number } = request;
  const repoKey = repositoryKeyToWire(repo.repositoryKey);
  const platformHost = resolvedPlatformHost(repo.provider, repo.platformHost);
  const pullBody = {
    provider: repo.provider,
    platform_host: platformHost,
    ...repoKey,
    owner: repo.owner,
    name: repo.name,
    mr_number: number,
  };
  const issueBody = {
    ...repoKey,
    ...(request.gitHeadRef ? { git_head_ref: request.gitHeadRef.trim() } : {}),
    ...(request.reuseExistingBranch ? { reuse_existing_branch: true } : {}),
    ...(request.reuseExistingDirectory ? { reuse_existing_directory: true } : {}),
  };
  const operation = `POST ${kind === "pull" ? "pull request" : "issue"} workspace`;
  if (hostKey?.startsWith("devbox:")) {
    return executeGeneratedApiRequest(operation, (client, signal) =>
      client.DevboxesService.createDevboxWorkspace(
        { connectionId: hostKey.slice(7) },
        kind === "pull"
          ? pullBody
          : {
              provider: repo.provider,
              platform_host: platformHost,
              ...repoKey,
              owner: repo.owner,
              name: repo.name,
              issue_number: number,
              branch: request.gitHeadRef ?? "",
              reuse_existing_branch: request.reuseExistingBranch ?? false,
            },
        { signal },
      ),
    ).pipe(Effect.map(created));
  }
  if (hostKey) {
    return executeOpaqueGeneratedApiRequest(operation, (client, signal) => {
      if (kind === "pull") return client.FleetService.createFleetWorkspace({ hostKey }, pullBody, { signal });
      return providerUsesHostRoute(repo)
        ? client.FleetService.createFleetIssueWorkspaceOnPlatformHost(
            { hostKey, ...providerHostRouteParams(repo), number: String(number) },
            issueBody,
            { signal },
          )
        : client.FleetService.createFleetIssueWorkspace(
            { hostKey, ...providerRouteParams(repo), number: String(number) },
            issueBody,
            { signal },
          );
    }).pipe(Effect.map(created));
  }
  return executeGeneratedApiRequest(operation, (client, signal) => {
    if (kind === "pull") return client.WorkspacesService.createWorkspace(pullBody, { signal });
    return providerUsesHostRoute(repo)
      ? client.IssuesService.createIssueWorkspaceOnHost({ ...providerHostRouteParams(repo), number }, issueBody, {
          signal,
        })
      : client.IssuesService.createIssueWorkspace({ ...providerRouteParams(repo), number }, issueBody, { signal });
  }).pipe(Effect.map(created));
}
