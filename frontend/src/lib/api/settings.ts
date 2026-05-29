import {
  normalizeTerminalSettings,
  type Settings,
} from "@middleman/ui/api/types";
import type { components } from "@middleman/ui/api/schema";
import {
  providerRepoPath,
  providerRouteParams,
} from "@middleman/ui/api/provider-routes";

import { apiErrorMessage, client } from "./runtime.js";

type SettingsResponse = components["schemas"]["SettingsResponse"];
type RepoPreviewGeneratedResponse =
  components["schemas"]["RepoPreviewResponse"];
type UpdateSettingsRequest =
  components["schemas"]["UpdateSettingsRequest"];
type SettingsActivityResponse = SettingsResponse["activity"];
type SettingsAgentResponse = NonNullable<SettingsResponse["agents"]>[number];
type SettingsRepoResponse = NonNullable<SettingsResponse["repos"]>[number];
type PreviewRepoResponse =
  NonNullable<RepoPreviewGeneratedResponse["repos"]>[number];

function requestErrorMessage(
  error: { detail?: string; title?: string } | undefined,
  fallback: string,
): string {
  return apiErrorMessage(error, fallback);
}

function normalizeActivityViewMode(
  viewMode: SettingsActivityResponse["view_mode"],
): Settings["activity"]["view_mode"] {
  return viewMode === "threaded" ? "threaded" : "flat";
}

function normalizeActivityTimeRange(
  timeRange: SettingsActivityResponse["time_range"],
): Settings["activity"]["time_range"] {
  return timeRange === "24h" || timeRange === "30d" || timeRange === "90d"
    ? timeRange
    : "7d";
}

function normalizeActivity(
  activity: SettingsActivityResponse,
): Settings["activity"] {
  return {
    view_mode: normalizeActivityViewMode(activity.view_mode),
    time_range: normalizeActivityTimeRange(activity.time_range),
    hide_closed: activity.hide_closed,
    hide_bots: activity.hide_bots,
    collapse_threads: activity.collapse_threads,
    default_branch_retention_days: activity.default_branch_retention_days,
    default_branch_max_commits: activity.default_branch_max_commits,
  };
}

function normalizeAgent(
  agent: SettingsAgentResponse,
): Settings["agents"][number] {
  const normalized: Settings["agents"][number] = {
    key: agent.key,
    label: agent.label,
  };

  if (agent.enabled !== undefined) {
    normalized.enabled = agent.enabled;
  }
  if (agent.command !== null) {
    normalized.command = agent.command;
  }

  return normalized;
}

function normalizeConfiguredRepo(
  repo: SettingsRepoResponse,
): Settings["repos"][number] {
  return {
    provider: repo.provider,
    platform_host: repo.platform_host,
    owner: repo.owner,
    name: repo.name,
    repo_path: repo.repo_path,
    is_glob: repo.is_glob,
    matched_repo_count: repo.matched_repo_count,
  };
}

function normalizeSettings(data: SettingsResponse): Settings {
  return {
    activity: normalizeActivity(data.activity),
    terminal: normalizeTerminalSettings(data.terminal),
    agents: (data.agents ?? []).map(normalizeAgent),
    repos: (data.repos ?? []).map(normalizeConfiguredRepo),
  };
}

export interface RepoPreviewRow {
  provider: string;
  platform_host: string;
  owner: string;
  name: string;
  repo_path: string;
  description: string | null;
  private: boolean;
  fork: boolean;
  pushed_at: string | null;
  already_configured: boolean;
}

export interface RepoPreviewResponse {
  provider: string;
  platform_host: string;
  owner: string;
  pattern: string;
  repos: RepoPreviewRow[];
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

function normalizePreviewResponse(
  data: RepoPreviewGeneratedResponse,
): RepoPreviewResponse {
  return {
    provider: data.provider,
    platform_host: data.platform_host,
    owner: data.owner,
    pattern: data.pattern,
    repos: (data.repos ?? []).map(normalizePreviewRow),
  };
}

function normalizePreviewRow(repo: PreviewRepoResponse): RepoPreviewRow {
  return {
    provider: repo.provider,
    platform_host: repo.platform_host,
    owner: repo.owner,
    name: repo.name,
    repo_path: repo.repo_path,
    description: repo.description,
    private: repo.private,
    fork: repo.fork,
    pushed_at: repo.pushed_at,
    already_configured: repo.already_configured,
  };
}

function normalizeUpdateRequest(
  settings: {
    activity?: Settings["activity"];
    terminal?: Settings["terminal"];
    agents?: Settings["agents"];
  },
): UpdateSettingsRequest {
  const request: UpdateSettingsRequest = {};
  if (settings.activity) {
    request.activity = settings.activity;
  }
  if (settings.terminal) {
    request.terminal = settings.terminal;
  }
  if (settings.agents) {
    request.agents = settings.agents.map((agent) => ({
      ...agent,
      command: agent.command ?? null,
    }));
  }
  return request;
}

export async function getSettings(): Promise<Settings> {
  const { data, error, response } = await client.GET("/settings");
  if (!data) {
    throw new Error(
      requestErrorMessage(
        error,
        `GET /settings -> ${response.status}`,
      ),
    );
  }
  return normalizeSettings(data);
}

export async function updateSettings(
  settings: {
    activity?: Settings["activity"];
    terminal?: Settings["terminal"];
    agents?: Settings["agents"];
  },
): Promise<Settings> {
  const { data, error, response } = await client.PUT("/settings", {
    body: normalizeUpdateRequest(settings),
  });
  if (!data) {
    throw new Error(
      requestErrorMessage(
        error,
        `PUT /settings -> ${response.status}`,
      ),
    );
  }
  return normalizeSettings(data);
}

export async function addRepo(
  owner: string,
  name: string,
  options: RepoRequestOptions,
): Promise<Settings> {
  const { data, error, response } = await client.POST("/repos", {
    body: { ...options, owner, name },
  });
  if (!data) {
    throw new Error(
      requestErrorMessage(error, `POST /repos -> ${response.status}`),
    );
  }
  return normalizeSettings(data);
}

export async function removeRepo(
  owner: string,
  name: string,
  options: RepoRequestOptions,
): Promise<void> {
  const ref = {
    provider: options.provider,
    platformHost: options.host,
    owner,
    name,
    repoPath: `${owner}/${name}`,
  };
  const { error, response } = await client.DELETE(
    providerRepoPath(ref),
    {
      params: { path: providerRouteParams(ref) },
    },
  );
  if (!response.ok) {
    throw new Error(
      requestErrorMessage(
        error,
        `DELETE /repos/{owner}/{name} -> ${response.status}`,
      ),
    );
  }
}

export async function refreshRepo(
  owner: string,
  name: string,
  options: RepoRequestOptions,
): Promise<Settings> {
  const ref = {
    provider: options.provider,
    platformHost: options.host,
    owner,
    name,
    repoPath: `${owner}/${name}`,
  };
  const { data, error, response } = await client.POST(
    providerRepoPath(ref, "/refresh"),
    {
      params: { path: providerRouteParams(ref) },
    },
  );
  if (!data) {
    throw new Error(
      requestErrorMessage(
        error,
        `POST /repos/{owner}/{name}/refresh -> ${response.status}`,
      ),
    );
  }
  return normalizeSettings(data);
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
    throw new Error(
      requestErrorMessage(error, `POST /repos/preview -> ${response.status}`),
    );
  }
  return normalizePreviewResponse(data);
}

export async function bulkAddRepos(repos: RepoInput[]): Promise<Settings> {
  const { data, error, response } = await client.POST("/repos/bulk", {
    body: {
      repos,
    },
  });
  if (!data) {
    throw new Error(
      requestErrorMessage(error, `POST /repos/bulk -> ${response.status}`),
    );
  }
  return normalizeSettings(data);
}
