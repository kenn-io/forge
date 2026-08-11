import type { components } from "../generated/schema.js";

import { apiErrorMessage, client } from "../runtime.js";

export type KataDaemonInfo = components["schemas"]["KataDaemonResponse"];
export type KataIssueReference = components["schemas"]["KataIssueReference"];
export type KataResolvedIssueReference = components["schemas"]["KataResolvedIssueReference"];
export type KataReferenceSearch = (
  daemonID: string,
  query: string,
  signal?: AbortSignal,
) => Promise<readonly KataIssueReference[]>;
export type KataWorkspaceIdentity = components["schemas"]["KataWorkspaceTaskRequest"];
export type KataWorkspaceResponse = components["schemas"]["WorkspaceResponse"];
export type KataLaunchTarget = components["schemas"]["KataLaunchTarget"];
export type KataProjectMappingDiagnostic = components["schemas"]["KataProjectMappingDiagnostic"];
export type KataProjectMappingsResponse = components["schemas"]["KataProjectMappingsResponse"];

function requestError(error: unknown, fallback: string): Error {
  return new Error(apiErrorMessage(error as { detail?: string; title?: string } | undefined, fallback));
}

export async function fetchKataDaemons(signal?: AbortSignal): Promise<KataDaemonInfo[]> {
  const result = await client.GET("/kata/daemons", signal ? { signal } : {});
  if (!result.data) throw requestError(result.error, "Unable to load Kata daemons.");
  return result.data.daemons ?? [];
}

export async function searchKataReferences(
  daemonID: string,
  query: string,
  signal?: AbortSignal,
): Promise<KataIssueReference[]> {
  const result = await client.GET("/kata/daemons/{daemon_id}/references", {
    params: {
      path: { daemon_id: daemonID },
      query: { q: query, limit: 50 },
    },
    ...(signal ? { signal } : {}),
  });
  if (!result.data) throw requestError(result.error, "Unable to search Kata issues.");
  return result.data.issues;
}

export async function resolveKataIssueReference(
  daemonID: string,
  issueUID: string,
  signal?: AbortSignal,
): Promise<KataIssueReference> {
  const result = await client.GET("/kata/daemons/{daemon_id}/references", {
    params: {
      path: { daemon_id: daemonID },
      query: { issue_uid: [issueUID], limit: 2 },
    },
    ...(signal ? { signal } : {}),
  });
  if (!result.data) throw requestError(result.error, "Unable to resolve Kata issue.");
  const matches = result.data.issues.filter((candidate) => candidate.uid === issueUID);
  if (matches.length !== 1) throw new Error("Unable to resolve Kata issue.");
  return matches[0]!;
}

export async function resolveKataTextReference(
  daemonID: string,
  project: string | undefined,
  reference: string,
  signal?: AbortSignal,
): Promise<KataResolvedIssueReference> {
  const result = await client.GET("/kata/daemons/{daemon_id}/issue-reference", {
    params: {
      path: { daemon_id: daemonID },
      query: { ...(project ? { project } : {}), ref: reference },
    },
    ...(signal ? { signal } : {}),
  });
  if (!result.data) throw requestError(result.error, "Unable to resolve Kata issue.");
  return result.data;
}

export async function createOrOpenKataWorkspace(identity: KataWorkspaceIdentity): Promise<KataWorkspaceResponse> {
  const result = await client.POST("/kata/workspaces", { body: identity });
  if (!result.data) throw requestError(result.error, "Unable to create or open Kata workspace.");
  return result.data;
}

export async function resolveKataLaunchTarget(daemonID: string, issueUID: string): Promise<KataLaunchTarget> {
  const result = await client.GET("/kata/daemons/{daemon_id}/issues/{issue_uid}/launch-target", {
    params: { path: { daemon_id: daemonID, issue_uid: issueUID } },
  });
  if (!result.data) throw requestError(result.error, "Unable to resolve Kata issue launch target.");
  return result.data;
}

export async function getKataProjectMappings(daemonID?: string): Promise<KataProjectMappingsResponse> {
  const result = await client.GET("/kata/project-mappings", {
    params: daemonID ? { header: { "X-Kenn-Forge-Kata-Daemon": daemonID } } : {},
  });
  if (!result.data) throw requestError(result.error, "Unable to load Kata project mappings.");
  return result.data;
}
