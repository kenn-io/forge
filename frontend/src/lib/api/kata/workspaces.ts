import type { components } from "@middleman/ui/api/schema";

import { apiErrorMessage, client } from "../runtime.js";
import { KATA_DAEMON_HEADER } from "./daemons.js";
import type { KataTaskSummary } from "./taskTypes.js";

export type KataWorkspaceTaskIdentity = components["schemas"]["KataWorkspaceTaskRequest"];
export type KataWorkspaceTarget = components["schemas"]["KataWorkspaceTargetResponse"];
export type KataWorkspaceMetadata = components["schemas"]["WorkspaceKataMetadata"];
export type KataWorkspaceResponse = components["schemas"]["WorkspaceResponse"] & {
  item_type: "kata_task";
  kata?: KataWorkspaceMetadata;
};
export type KataProjectMappingDiagnostic = components["schemas"]["KataProjectMappingDiagnostic"];
export type KataProjectMappingsResponse = components["schemas"]["KataProjectMappingsResponse"];

function requestErrorMessage(error: { detail?: string; title?: string } | undefined, fallback: string): string {
  return apiErrorMessage(error, fallback);
}

export function kataWorkspaceIdentityFromIssue(
  issue: KataTaskSummary,
  daemonID: string | null | undefined,
  projectName?: string | null,
): KataWorkspaceTaskIdentity {
  const trimmedDaemonID = daemonID?.trim() ?? "";
  const trimmedProjectName = projectName?.trim() || issue.project_name;
  const identity: KataWorkspaceTaskIdentity = {
    daemon_id: trimmedDaemonID,
    project_uid: issue.project_uid,
    issue_uid: issue.uid,
  };
  if (trimmedProjectName !== "") identity.project_name = trimmedProjectName;
  if (issue.short_id !== "") identity.short_id = issue.short_id;
  if (issue.qualified_id !== "") identity.qualified_id = issue.qualified_id;
  if (issue.title !== "") identity.title = issue.title;
  return identity;
}

export function createKataWorkspaceForTask(identity: KataWorkspaceTaskIdentity): Promise<KataWorkspaceResponse> {
  return client
    .POST("/kata/workspaces", {
      body: identity,
    })
    .then(({ data, error, response }) => {
      if (!data) {
        throw new Error(requestErrorMessage(error, `POST /kata/workspaces -> ${response.status}`));
      }
      return data as KataWorkspaceResponse;
    });
}

export async function getKataProjectMappings(daemonID?: string): Promise<KataProjectMappingsResponse> {
  const { data, error, response } = await client.GET("/kata/project-mappings", {
    params: daemonID ? { header: { [KATA_DAEMON_HEADER]: daemonID } } : {},
  });
  if (!data) {
    throw new Error(requestErrorMessage(error, `GET /kata/project-mappings -> ${response.status}`));
  }
  return data;
}
