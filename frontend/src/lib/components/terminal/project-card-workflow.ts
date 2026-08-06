import { Effect, Schema } from "effect";
import type { components } from "../../api/generated/schema.js";

import { InvalidExternalPayload, type ApiProblemError, type TransientTransportError } from "../../api/effect-errors.js";
import { executeGeneratedApiRequest, executeOpaqueGeneratedApiRequest } from "../../api/generated-api.js";
import { decodeProjectResponse } from "../../api/project-intake.js";

export type WorkspaceProject = components["schemas"]["ProjectResponse"];
export type WorkspaceProjectWorktree = components["schemas"]["WorktreeResponse"];

export interface ProjectCardSnapshot {
  readonly project: WorkspaceProject;
  readonly worktrees: readonly WorkspaceProjectWorktree[];
}

export type ProjectCardFailure = ApiProblemError | InvalidExternalPayload | TransientTransportError;

export function projectCardFailureMessage(failure: ProjectCardFailure): string {
  switch (failure._tag) {
    case "ApiProblemError":
      return failure.problem.detail ?? failure.problem.title ?? "Couldn't load this project.";
    case "InvalidExternalPayload":
      return "The project service returned an invalid response.";
    case "TransientTransportError":
      return "Couldn't reach the project service.";
  }
}

const Worktree = Schema.Struct({
  $schema: Schema.optionalKey(Schema.String),
  branch: Schema.String,
  created_at: Schema.String,
  id: Schema.String,
  is_hidden: Schema.Boolean,
  is_primary: Schema.Boolean,
  linked_issue_numbers: Schema.NullOr(Schema.Array(Schema.Number)),
  path: Schema.String,
  project_id: Schema.String,
  session_backend: Schema.String,
  updated_at: Schema.String,
});

const WorktreeList = Schema.Struct({
  $schema: Schema.optionalKey(Schema.String),
  worktrees: Schema.NullOr(Schema.Array(Worktree)),
});

const decodeWorktreeList = Effect.fn("ProjectCard.decodeWorktreeList")(function* (input: unknown) {
  const decoded = yield* Schema.decodeUnknownEffect(WorktreeList)(input).pipe(
    Effect.mapError((cause) => InvalidExternalPayload.make({ operation: "decode fleet project worktrees", cause })),
  );
  const worktrees: readonly WorkspaceProjectWorktree[] = (decoded.worktrees ?? []).map((worktree) => ({
    ...worktree,
    linked_issue_numbers: worktree.linked_issue_numbers === null ? null : [...worktree.linked_issue_numbers],
  }));
  return worktrees;
});

export const loadProjectCardSnapshot = Effect.fn("ProjectCard.loadSnapshot")(function* (
  projectId: string,
  hostKey?: string,
) {
  const project = hostKey
    ? yield* executeOpaqueGeneratedApiRequest("load fleet project", (client, signal) =>
        client.GET("/fleet/hosts/{host_key}/projects/{project_id}", {
          params: { path: { host_key: hostKey, project_id: projectId } },
          signal,
        }),
      ).pipe(Effect.flatMap(decodeProjectResponse))
    : yield* executeGeneratedApiRequest("load project", (client, signal) =>
        client.GET("/projects/{project_id}", {
          params: { path: { project_id: projectId } },
          signal,
        }),
      );

  const worktrees = hostKey
    ? yield* executeOpaqueGeneratedApiRequest("load fleet project worktrees", (client, signal) =>
        client.GET("/fleet/hosts/{host_key}/projects/{project_id}/worktrees", {
          params: { path: { host_key: hostKey, project_id: projectId } },
          signal,
        }),
      ).pipe(Effect.flatMap(decodeWorktreeList))
    : yield* executeGeneratedApiRequest("load project worktrees", (client, signal) =>
        client.GET("/projects/{project_id}/worktrees", {
          params: { path: { project_id: projectId } },
          signal,
        }),
      ).pipe(Effect.map((data) => data.worktrees ?? []));

  return { project, worktrees } satisfies ProjectCardSnapshot;
});
