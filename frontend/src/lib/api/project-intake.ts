import type { components } from "@middleman/ui/api/schema";

import { apiErrorMessage, client } from "./runtime.ts";

export type ProjectResponse = components["schemas"]["ProjectResponse"];
export type UserRepository = components["schemas"]["UserRepository"];

export async function registerExistingProject(
  path: string,
): Promise<ProjectResponse> {
  const trimmed = path.trim();
  if (!trimmed) {
    throw new Error("Repository path is required.");
  }

  const { data: validation, error: validationError } =
    await client.GET("/filesystem/validate-repo", {
      params: { query: { path: trimmed } },
    });
  if (!validation) {
    throw new Error(
      apiErrorMessage(
        validationError,
        "Couldn't validate repository path.",
      ),
    );
  }
  if (!validation.is_valid) {
    throw new Error(validation.message ?? "Not a git repository.");
  }

  const { data, error } = await client.POST("/projects", {
    body: { local_path: validation.root_path ?? trimmed },
  });
  if (!data) {
    throw new Error(
      apiErrorMessage(error, "Couldn't register repository."),
    );
  }
  return data;
}

export async function cloneProject(
  url: string,
  path: string,
  branch?: string,
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

  const { data, error } = await client.POST("/projects/clone", {
    body: {
      url: trimmedURL,
      path: trimmedPath,
      ...(trimmedBranch ? { branch: trimmedBranch } : {}),
    },
  });
  if (!data) {
    throw new Error(apiErrorMessage(error, "Couldn't clone repository."));
  }
  return data;
}

export async function listUserRepositories(): Promise<UserRepository[]> {
  const { data, error } = await client.GET("/platform/user-repositories", {
    params: { query: { limit: 100 } },
  });
  if (!data) {
    throw new Error(
      apiErrorMessage(error, "Couldn't load GitHub repositories."),
    );
  }
  return data.repositories ?? [];
}
