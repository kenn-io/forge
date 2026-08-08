import { Effect } from "effect";
import type { components } from "../generated/schema.js";

import { InvalidExternalPayload } from "../effect-errors.js";
import { executeGeneratedApiRequest } from "../generated-api.js";
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

export const createKataWorkspaceForTask = Effect.fn("KataWorkspaces.createForTask")(function* (
  identity: KataWorkspaceTaskIdentity,
) {
  const workspace = yield* executeGeneratedApiRequest("create Kata task workspace", (client, signal) =>
    client.POST("/kata/workspaces", { body: identity, signal }),
  );
  if (workspace.item_type !== "kata_task") {
    return yield* Effect.fail(
      InvalidExternalPayload.make({
        operation: "create Kata task workspace",
        cause: new Error(`expected kata_task workspace, received ${workspace.item_type}`),
      }),
    );
  }
  return workspace;
});

export const getKataProjectMappings = Effect.fn("KataWorkspaces.projectMappings")(function* (daemonID?: string) {
  return yield* executeGeneratedApiRequest("load Kata project mappings", (client, signal) =>
    client.GET("/kata/project-mappings", {
      params: daemonID ? { header: { [KATA_DAEMON_HEADER]: daemonID } } : {},
      signal,
    }),
  );
});
