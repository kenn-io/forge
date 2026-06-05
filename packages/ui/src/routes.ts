import { providerRouteParams, type ProviderRouteRef as APIProviderRouteRef } from "./api/provider-routes.js";

export type RepositoryRouteRef = {
  provider: string;
  platformHost?: string | undefined;
  owner: string;
  name: string;
  repoPath: string;
};

export type ProviderRouteRef = {
  provider: string;
  platformHost?: string | undefined;
  repoPath: string;
};

export type NumberedRouteItemRef = RepositoryRouteRef & {
  number: number;
};

export type PullRequestRouteRef = NumberedRouteItemRef;

export type IssueRouteRef = NumberedRouteItemRef;

export type RoutedItemRef = (PullRequestRouteRef & { itemType: "pr" }) | (IssueRouteRef & { itemType: "issue" });

export type FocusListRouteRef = {
  itemType: "mrs" | "issues";
  repo?: string | undefined;
};

export function buildPullRequestRoute(ref: PullRequestRouteRef): string {
  return buildProviderPullRequestRoute(providerRouteRef(ref));
}

export function buildPullRequestFilesRoute(ref: PullRequestRouteRef): string {
  return buildProviderPullRequestFilesRoute(providerRouteRef(ref));
}

export function buildProviderPullRequestRoute(ref: ProviderRouteRef & { number: number }): string {
  return providerItemPath("pulls", ref);
}

export function buildProviderPullRequestFilesRoute(ref: ProviderRouteRef & { number: number }): string {
  return `${providerItemPath("pulls", ref)}/files`;
}

export function buildIssueRoute(ref: IssueRouteRef): string {
  return buildProviderIssueRoute(providerRouteRef(ref));
}

export function buildProviderIssueRoute(ref: ProviderRouteRef & { number: number }): string {
  return providerItemPath("issues", ref);
}

export function buildFocusPullRequestRoute(ref: PullRequestRouteRef): string {
  return `/focus${buildProviderPullRequestRoute(providerRouteRef(ref))}`;
}

export function buildFocusPullRequestFilesRoute(ref: PullRequestRouteRef): string {
  return `/focus${buildProviderPullRequestFilesRoute(providerRouteRef(ref))}`;
}

export function buildFocusIssueRoute(ref: IssueRouteRef): string {
  return `/focus${buildProviderIssueRoute(providerRouteRef(ref))}`;
}

export function buildFocusListRoute(ref: FocusListRouteRef): string {
  const route = `/focus/${ref.itemType}`;
  return ref.repo ? `${route}?repo=${encodeURIComponent(ref.repo)}` : route;
}

export function buildRoutedItemRoute(ref: RoutedItemRef, options: { focus?: boolean } = {}): string {
  if (ref.itemType === "pr") {
    return options.focus ? buildFocusPullRequestRoute(ref) : buildPullRequestRoute(ref);
  }
  return options.focus ? buildFocusIssueRoute(ref) : buildIssueRoute(ref);
}

function requireRouteText(value: string, field: string): string {
  if (!value) {
    throw new Error(`missing route ${field}`);
  }
  return value;
}

function providerRouteRef(ref: NumberedRouteItemRef): ProviderRouteRef & { number: number } {
  return {
    provider: ref.provider,
    platformHost: ref.platformHost,
    repoPath: ref.repoPath,
    number: ref.number,
  };
}

function providerItemPath(kind: "pulls" | "issues", ref: ProviderRouteRef & { number: number }): string {
  const routeRef = providerRouteParts(ref);
  const encodedProvider = encodeURIComponent(routeRef.provider);
  const encodedOwner = encodeURIComponent(routeRef.owner);
  const encodedName = encodeURIComponent(routeRef.name);
  const encodedNumber = encodeURIComponent(ref.number.toString());
  if (routeRef.platform_host) {
    return `/host/${encodeURIComponent(routeRef.platform_host)}/${kind}/${encodedProvider}/${encodedOwner}/${encodedName}/${encodedNumber}`;
  }
  return `/${kind}/${encodedProvider}/${encodedOwner}/${encodedName}/${encodedNumber}`;
}

function providerRouteParts(ref: ProviderRouteRef): ReturnType<typeof providerRouteParams> {
  const repoPath = requireRouteText(ref.repoPath, "repoPath").replace(/^\/+|\/+$/g, "");
  const pathParts = repoPath.split("/").filter(Boolean);
  if (pathParts.length < 2) {
    throw new Error("missing route repoPath owner/name");
  }
  const owner = pathParts.slice(0, -1).join("/");
  const name = pathParts[pathParts.length - 1]!;
  return providerRouteParams({
    provider: requireRouteText(ref.provider, "provider"),
    platformHost: ref.platformHost,
    owner,
    name,
    repoPath,
  } satisfies APIProviderRouteRef);
}
