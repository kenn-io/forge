import type { components } from "@middleman/ui/api/schema";

import { apiErrorMessage, client } from "../runtime.js";
import type { KataTaskSummary } from "./taskTypes.js";

export type KataWorkspaceTaskIdentity = components["schemas"]["KataWorkspaceTaskRequest"];
export type KataWorkspaceTarget = components["schemas"]["KataWorkspaceTargetResponse"];
export type KataWorkspaceMetadata = components["schemas"]["WorkspaceKataMetadata"];
export type KataWorkspaceResponse = components["schemas"]["WorkspaceResponse"] & {
  item_type: "kata_task";
  kata?: KataWorkspaceMetadata;
};

function requestErrorMessage(error: { detail?: string; title?: string } | undefined, fallback: string): string {
  return apiErrorMessage(error, fallback);
}

export function kataWorkspaceIdentityFromIssue(
  issue: KataTaskSummary,
  daemonID: string | null | undefined,
): KataWorkspaceTaskIdentity {
  const identity: KataWorkspaceTaskIdentity = {
    project_uid: issue.project_uid,
    issue_uid: issue.uid,
  };
  const trimmedDaemonID = daemonID?.trim() ?? "";
  if (trimmedDaemonID !== "") identity.daemon_id = trimmedDaemonID;
  if (issue.project_name !== "") identity.project_name = issue.project_name;
  if (issue.short_id !== "") identity.short_id = issue.short_id;
  if (issue.qualified_id !== "") identity.qualified_id = issue.qualified_id;
  if (issue.title !== "") identity.title = issue.title;
  return identity;
}

export function resolveKataWorkspaceTarget(identity: KataWorkspaceTaskIdentity): Promise<KataWorkspaceTarget> {
  return client
    .POST("/kata/workspace-target", {
      body: identity,
    })
    .then(({ data, error, response }) => {
      if (!data) {
        throw new Error(requestErrorMessage(error, `POST /kata/workspace-target -> ${response.status}`));
      }
      return data;
    });
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
