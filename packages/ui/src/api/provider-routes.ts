export type ProviderRouteRef = {
  provider: string;
  platformHost?: string | undefined;
  owner: string;
  name: string;
  repoPath: string;
};

type RouteKind = "pulls" | "issues" | "repo";

const defaultHosts: Record<string, string> = {
  github: "github.com",
  gh: "github.com",
  gitlab: "gitlab.com",
  gl: "gitlab.com",
  forgejo: "codeberg.org",
  fj: "codeberg.org",
  gitea: "gitea.com",
};

export function canonicalProvider(provider: string): string {
  const normalized = provider.toLowerCase();
  if (normalized === "gh") return "github";
  if (normalized === "gl") return "gitlab";
  if (normalized === "fj") return "forgejo";
  return normalized;
}

function defaultHost(provider: string): string | undefined {
  return defaultHosts[provider.toLowerCase()];
}

function shouldUseHostRoute(ref: ProviderRouteRef): boolean {
  const provider = canonicalProvider(ref.provider);
  const host = ref.platformHost?.trim();
  return !!host && host !== defaultHost(provider);
}

export function providerRouteParams(ref: ProviderRouteRef) {
  return {
    provider: canonicalProvider(ref.provider),
    owner: ref.owner,
    name: ref.name,
    ...(shouldUseHostRoute(ref) && {
      platform_host: ref.platformHost?.trim(),
    }),
  };
}

type PullSuffix =
  | ""
  | "/approve"
  | "/approve-workflows"
  | "/comments"
  | "/comments/{comment_id}"
  | "/commits"
  | "/diff"
  | "/files"
  | "/file-preview"
  | "/github-state"
  | "/ci-refresh"
  | "/import-metadata"
  | "/labels"
  | "/merge"
  | "/ready-for-review"
  | "/discussions/{discussion_id}/reply"
  | "/review-draft"
  | "/review-draft/comments"
  | "/review-draft/comments/{draft_comment_id}"
  | "/review-draft/publish"
  | "/review-threads/{thread_id}/resolve"
  | "/review-threads/{thread_id}/unresolve"
  | "/stack"
  | "/state"
  | "/sync"
  | "/sync/async";

type IssueSuffix =
  | ""
  | "/comments"
  | "/comments/{comment_id}"
  | "/github-state"
  | "/labels"
  | "/sync"
  | "/sync/async"
  | "/workspace";

type PullPath<S extends PullSuffix> =
  | `/pulls/{provider}/{owner}/{name}/{number}${S}`
  | `/host/{platform_host}/pulls/{provider}/{owner}/{name}/{number}${S}`;

type IssuePath<S extends IssueSuffix> =
  | `/issues/{provider}/{owner}/{name}/{number}${S}`
  | `/host/{platform_host}/issues/{provider}/{owner}/{name}/{number}${S}`;

export function providerItemPath(
  kind: "pulls",
  ref: ProviderRouteRef,
): PullPath<"">;
export function providerItemPath<S extends PullSuffix>(
  kind: "pulls",
  ref: ProviderRouteRef,
  suffix: S,
): PullPath<S>;
export function providerItemPath(
  kind: "issues",
  ref: ProviderRouteRef,
): IssuePath<"">;
export function providerItemPath<S extends IssueSuffix>(
  kind: "issues",
  ref: ProviderRouteRef,
  suffix: S,
): IssuePath<S>;
export function providerItemPath(
  kind: "pulls" | "issues",
  ref: ProviderRouteRef,
  suffix = "",
): string {
  if (shouldUseHostRoute(ref)) {
    return `/host/{platform_host}/${kind}/{provider}/{owner}/{name}/{number}${suffix}`;
  }
  return `/${kind}/{provider}/{owner}/{name}/{number}${suffix}`;
}

type RepoSuffix =
  | ""
  | "/comment-autocomplete"
  | "/commits/{sha}/diff"
  | "/labels"
  | "/refresh"
  | "/resolve/{number}";

type RepoPath<S extends RepoSuffix> =
  | `/repo/{provider}/{owner}/{name}${S}`
  | `/host/{platform_host}/repo/{provider}/{owner}/{name}${S}`;

export function providerRepoPath(ref: ProviderRouteRef): RepoPath<"">;
export function providerRepoPath<S extends RepoSuffix>(
  ref: ProviderRouteRef,
  suffix: S,
): RepoPath<S>;
export function providerRepoPath(ref: ProviderRouteRef, suffix = ""): string {
  if (shouldUseHostRoute(ref)) {
    return `/host/{platform_host}/repo/{provider}/{owner}/{name}${suffix}`;
  }
  return `/repo/{provider}/{owner}/{name}${suffix}`;
}

type CollectionKind = Exclude<RouteKind, "repo">;
type CollectionPath<K extends CollectionKind> =
  | `/${K}/{provider}/{owner}/{name}`
  | `/host/{platform_host}/${K}/{provider}/{owner}/{name}`;

export function providerCollectionPath<K extends CollectionKind>(
  kind: K,
  ref: ProviderRouteRef,
): CollectionPath<K>;
export function providerCollectionPath(
  kind: CollectionKind,
  ref: ProviderRouteRef,
): string {
  if (shouldUseHostRoute(ref)) {
    return `/host/{platform_host}/${kind}/{provider}/{owner}/{name}`;
  }
  return `/${kind}/{provider}/{owner}/{name}`;
}
