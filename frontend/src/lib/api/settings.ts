import type { Settings } from "@middleman/ui/api/types";
import type { components } from "@middleman/ui/api/schema";
import { providerRepoPath, providerRouteParams } from "@middleman/ui/api/provider-routes";

import { apiErrorMessage, client } from "./runtime.js";

type UpdateSettingsRequest = components["schemas"]["UpdateSettingsRequest"];
type UpdateFleetSettingsRequest = components["schemas"]["UpdateFleetSettingsInputBody"];
export type RepoPreviewResponse = components["schemas"]["RepoPreviewResponse"];
export type RepoPreviewRow = components["schemas"]["RepoPreviewRow"];

function requestErrorMessage(error: { detail?: string; title?: string } | undefined, fallback: string): string {
  return apiErrorMessage(error, fallback);
}

export interface RepoRequestOptions {
  provider: string;
  host?: string;
}

export interface RepoInput extends RepoRequestOptions {
  owner?: string;
  name?: string;
  repo_path?: string;
}

function normalizeUpdateRequest(settings: {
  activity?: Settings["activity"];
  modes?: Settings["modes"];
  terminal?: Settings["terminal"];
  agents?: Settings["agents"];
}): UpdateSettingsRequest {
  const request: UpdateSettingsRequest = {};
  if (settings.activity) {
    request.activity = settings.activity;
  }
  if (settings.modes) {
    request.modes = settings.modes;
  }
  if (settings.terminal) {
    request.terminal = settings.terminal;
  }
  if (settings.agents) {
    request.agents = settings.agents;
  }
  return request;
}

export async function getSettings(): Promise<Settings> {
  const { data, error, response } = await client.GET("/settings");
  if (!data) {
    throw new Error(requestErrorMessage(error, `GET /settings -> ${response.status}`));
  }
  return data;
}

export async function updateSettings(settings: {
  activity?: Settings["activity"];
  modes?: Settings["modes"];
  terminal?: Settings["terminal"];
  agents?: Settings["agents"];
}): Promise<Settings> {
  const { data, error, response } = await client.PUT("/settings", {
    body: normalizeUpdateRequest(settings),
  });
  if (!data) {
    throw new Error(requestErrorMessage(error, `PUT /settings -> ${response.status}`));
  }
  return data;
}

export async function updateFleetSettings(fleet: UpdateFleetSettingsRequest): Promise<Settings["fleet"]> {
  const { data, error, response } = await client.PUT("/settings/fleet", {
    body: fleet,
  });
  if (!data) {
    throw new Error(requestErrorMessage(error, `PUT /settings/fleet -> ${response.status}`));
  }
  return data;
}

export async function addRepo(owner: string, name: string, options: RepoRequestOptions): Promise<Settings> {
  const { data, error, response } = await client.POST("/repos", {
    body: { ...options, owner, name },
  });
  if (!data) {
    throw new Error(requestErrorMessage(error, `POST /repos -> ${response.status}`));
  }
  return data;
}

export async function removeRepo(owner: string, name: string, options: RepoRequestOptions): Promise<void> {
  const ref = {
    provider: options.provider,
    platformHost: options.host,
    owner,
    name,
    repoPath: `${owner}/${name}`,
  };
  const { error, response } = await client.DELETE(providerRepoPath(ref), {
    params: { path: providerRouteParams(ref) },
  });
  if (!response.ok) {
    throw new Error(requestErrorMessage(error, `DELETE /repos/{owner}/{name} -> ${response.status}`));
  }
}

export async function refreshRepo(owner: string, name: string, options: RepoRequestOptions): Promise<Settings> {
  const ref = {
    provider: options.provider,
    platformHost: options.host,
    owner,
    name,
    repoPath: `${owner}/${name}`,
  };
  const { data, error, response } = await client.POST(providerRepoPath(ref, "/refresh"), {
    params: { path: providerRouteParams(ref) },
  });
  if (!data) {
    throw new Error(requestErrorMessage(error, `POST /repos/{owner}/{name}/refresh -> ${response.status}`));
  }
  return data;
}

export async function updateRepoWorktreeBasePath(
  owner: string,
  name: string,
  options: RepoRequestOptions,
  worktreeBasePath: string,
): Promise<Settings> {
  const ref = {
    provider: options.provider,
    platformHost: options.host,
    owner,
    name,
    repoPath: `${owner}/${name}`,
  };
  const { data, error, response } = await client.PUT(providerRepoPath(ref, "/worktree-base"), {
    params: { path: providerRouteParams(ref) },
    body: { worktree_base_path: worktreeBasePath },
  });
  if (!data) {
    throw new Error(requestErrorMessage(error, `PUT /repos/{owner}/{name}/worktree-base -> ${response.status}`));
  }
  return data;
}

export async function previewRepos(
  owner: string,
  pattern: string,
  options: RepoRequestOptions,
): Promise<RepoPreviewResponse> {
  const { data, error, response } = await client.POST("/repos/preview", {
    body: { ...options, owner, pattern },
  });
  if (!data) {
    throw new Error(requestErrorMessage(error, `POST /repos/preview -> ${response.status}`));
  }
  return data;
}

export async function bulkAddRepos(repos: RepoInput[]): Promise<Settings> {
  const { data, error, response } = await client.POST("/repos/bulk", {
    body: {
      repos,
    },
  });
  if (!data) {
    throw new Error(requestErrorMessage(error, `POST /repos/bulk -> ${response.status}`));
  }
  return data;
}
