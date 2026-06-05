import type { PullRequest } from "../../api/types.js";
import type { ProviderRouteRef } from "../../api/provider-routes.js";

export type KanbanDragPayload = {
  provider: string;
  platformHost: string;
  owner: string;
  name: string;
  repoPath: string;
  number: number;
};

function requireText(value: string | undefined, field: string): string {
  const trimmed = value?.trim();
  if (!trimmed) {
    throw new Error(`kanban drag payload missing ${field}`);
  }
  return trimmed;
}

function requireNumber(value: number | undefined, field: string): number {
  if (typeof value !== "number" || !Number.isInteger(value) || value <= 0) {
    throw new Error(`kanban drag payload missing ${field}`);
  }
  return value;
}

export function kanbanDragPayloadFromPull(pr: PullRequest): KanbanDragPayload {
  return {
    provider: requireText(pr.repo.provider, "provider"),
    platformHost: requireText(pr.repo.platform_host, "platformHost"),
    owner: requireText(pr.repo.owner, "owner"),
    name: requireText(pr.repo.name, "name"),
    repoPath: requireText(pr.repo.repo_path, "repoPath"),
    number: requireNumber(pr.Number, "number"),
  };
}

export function providerRouteRefFromKanbanDragPayload(payload: KanbanDragPayload): ProviderRouteRef {
  return {
    provider: requireText(payload.provider, "provider"),
    platformHost: requireText(payload.platformHost, "platformHost"),
    owner: requireText(payload.owner, "owner"),
    name: requireText(payload.name, "name"),
    repoPath: requireText(payload.repoPath, "repoPath"),
  };
}

export function parseKanbanDragPayload(raw: string): KanbanDragPayload {
  const parsed = JSON.parse(raw) as Partial<KanbanDragPayload>;
  return {
    provider: requireText(parsed.provider, "provider"),
    platformHost: requireText(parsed.platformHost, "platformHost"),
    owner: requireText(parsed.owner, "owner"),
    name: requireText(parsed.name, "name"),
    repoPath: requireText(parsed.repoPath, "repoPath"),
    number: requireNumber(parsed.number, "number"),
  };
}
