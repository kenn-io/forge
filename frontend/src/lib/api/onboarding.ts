import type { PullRequest } from "@kenn-forge/ui/api/types";

import { apiErrorMessage, client } from "./runtime.ts";

export interface CreatedWorkspace {
  id: string;
  status: string;
}

export async function createPullRequestWorkspace(pull: PullRequest): Promise<CreatedWorkspace> {
  const { data, error } = await client.POST("/workspaces", {
    body: {
      provider: pull.repo.provider,
      platform_host: pull.repo.platform_host,
      owner: pull.repo.owner,
      name: pull.repo.name,
      mr_number: pull.Number,
    },
  });
  if (!data?.id) {
    throw new Error(apiErrorMessage(error, "Could not create workspace"));
  }
  return {
    id: data.id,
    status: data.status,
  };
}
