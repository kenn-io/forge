import type { components } from "@middleman/ui/api/schema";

import { apiErrorMessage, client } from "./runtime.ts";

export type ProjectResponse = components["schemas"]["ProjectResponse"];
export type UserRepository = components["schemas"]["UserRepository"];
type ProblemError = components["schemas"]["ProblemError"];
type RepoValidation = components["schemas"]["FilesystemValidateRepoOutputBody"];

export interface ProjectIntakeOptions {
  hostKey?: string | null;
}

function normalizedHostKey(options?: ProjectIntakeOptions): string | undefined {
  const hostKey = options?.hostKey?.trim();
  return hostKey ? hostKey : undefined;
}

export async function registerExistingProject(path: string, options?: ProjectIntakeOptions): Promise<ProjectResponse> {
  const trimmed = path.trim();
  if (!trimmed) {
    throw new Error("Repository path is required.");
  }

  const hostKey = normalizedHostKey(options);
  const validationResult = hostKey
    ? await client.GET("/fleet/hosts/{host_key}/filesystem/validate-repo", {
        params: { path: { host_key: hostKey }, query: { path: trimmed } },
      })
    : await client.GET("/filesystem/validate-repo", {
        params: { query: { path: trimmed } },
      });
  const validation = validationResult.data as RepoValidation | undefined;
  if (!validation) {
    throw new Error(
      apiErrorMessage(validationResult.error as ProblemError | undefined, "Couldn't validate repository path."),
    );
  }
  if (!validation.is_valid) {
    throw new Error(validation.message ?? "Not a git repository.");
  }

  const body = { local_path: validation.root_path ?? trimmed };
  const result = hostKey
    ? await client.POST("/fleet/hosts/{host_key}/projects", {
        params: { path: { host_key: hostKey } },
        body,
      })
    : await client.POST("/projects", { body });
  const data = result.data as ProjectResponse | undefined;
  if (!data) {
    throw new Error(apiErrorMessage(result.error as ProblemError | undefined, "Couldn't register repository."));
  }
  return data;
}

export async function cloneProject(
  url: string,
  path: string,
  branch?: string,
  options?: ProjectIntakeOptions,
): Promise<ProjectResponse> {
  const trimmedURL = url.trim();
  const trimmedPath = path.trim();
  const trimmedBranch = branch?.trim();
  if (!trimmedURL) {
    throw new Error("Repository URL is required.");
  }
  if (!trimmedPath) {
    throw new Error("Destination path is required.");
  }

  const hostKey = normalizedHostKey(options);
  const body = {
    url: trimmedURL,
    path: trimmedPath,
    ...(trimmedBranch ? { branch: trimmedBranch } : {}),
  };
  const result = hostKey
    ? await client.POST("/fleet/hosts/{host_key}/projects/clone", {
        params: { path: { host_key: hostKey } },
        body,
      })
    : await client.POST("/projects/clone", { body });
  const data = result.data as ProjectResponse | undefined;
  if (!data) {
    throw new Error(apiErrorMessage(result.error as ProblemError | undefined, "Couldn't clone repository."));
  }
  return data;
}

export async function listUserRepositories(): Promise<UserRepository[]> {
  const { data, error } = await client.GET("/platform/user-repositories", {
    params: { query: { limit: 100 } },
  });
  if (!data) {
    throw new Error(apiErrorMessage(error, "Couldn't load GitHub repositories."));
  }
  return data.repositories ?? [];
}
