import { canonicalProvider } from "../api/provider-routes.js";
import { buildIssueRoute, buildRoutedItemRoute, type RepositoryRouteRef } from "../routes.js";

export type ItemReferenceType = "pr" | "issue";

export type ResolvableItemReference = RepositoryRouteRef & {
  number: number;
  itemType?: ItemReferenceType | undefined;
  externalUrl?: string | undefined;
};

export type ItemReferenceDataAttributes = {
  "data-provider": string;
  "data-owner": string;
  "data-name": string;
  "data-repo-path": string;
  "data-number": string;
  "data-platform-host"?: string | undefined;
  "data-item-type"?: ItemReferenceType | undefined;
  "data-external-url"?: string | undefined;
};

export type ItemReferenceLink = {
  href: string;
  dataAttributes: ItemReferenceDataAttributes;
};

const defaultHosts: Record<string, string> = {
  github: "github.com",
  gitlab: "gitlab.com",
};

function providerHost(provider: string, platformHost: string | undefined): string | null {
  return platformHost?.trim() || defaultHosts[canonicalProvider(provider)] || null;
}

function encodeRepoPath(repoPath: string): string {
  return repoPath
    .replace(/^\/+|\/+$/g, "")
    .split("/")
    .filter(Boolean)
    .map((part) => encodeURIComponent(part))
    .join("/");
}

export function buildCanonicalProviderItemURL(ref: ResolvableItemReference): string | undefined {
  const host = providerHost(ref.provider, ref.platformHost);
  const repoPath = encodeRepoPath(ref.repoPath);
  if (!host || !repoPath) return undefined;
  const provider = canonicalProvider(ref.provider);
  const number = encodeURIComponent(ref.number.toString());
  let itemPath: string;
  if (ref.itemType === "pr") {
    if (provider === "gitlab") {
      itemPath = `/-/merge_requests/${number}`;
    } else if (provider === "github") {
      itemPath = `/pull/${number}`;
    } else {
      itemPath = `/pulls/${number}`;
    }
  } else {
    itemPath = provider === "gitlab" ? `/-/issues/${number}` : `/issues/${number}`;
  }
  return `https://${host}/${repoPath}${itemPath}`;
}

export function buildItemReferenceHref(ref: ResolvableItemReference): string {
  if (ref.itemType === "pr") {
    return buildRoutedItemRoute({ ...ref, itemType: "pr" });
  }
  if (ref.itemType === "issue") {
    return buildRoutedItemRoute({ ...ref, itemType: "issue" });
  }
  return buildIssueRoute(ref);
}

export function itemReferenceDataAttributes(ref: ResolvableItemReference): ItemReferenceDataAttributes {
  const externalUrl = ref.externalUrl ?? buildCanonicalProviderItemURL(ref);
  return {
    "data-provider": ref.provider,
    ...(ref.platformHost && {
      "data-platform-host": ref.platformHost,
    }),
    "data-owner": ref.owner,
    "data-name": ref.name,
    "data-repo-path": ref.repoPath,
    "data-number": ref.number.toString(),
    ...(ref.itemType && {
      "data-item-type": ref.itemType,
    }),
    ...(externalUrl && {
      "data-external-url": externalUrl,
    }),
  };
}

export function buildItemReferenceLink(ref: ResolvableItemReference): ItemReferenceLink {
  return {
    href: buildItemReferenceHref(ref),
    dataAttributes: itemReferenceDataAttributes(ref),
  };
}

function escapeAttribute(value: string): string {
  return value.replaceAll("&", "&amp;").replaceAll('"', "&quot;").replaceAll("<", "&lt;").replaceAll(">", "&gt;");
}

export function itemReferenceAnchorAttributes(ref: ResolvableItemReference, className = "item-ref"): string {
  const link = buildItemReferenceLink(ref);
  const attrs: Array<[string, string | undefined]> = [
    ["class", className],
    ["href", link.href],
    ["data-provider", link.dataAttributes["data-provider"]],
    ["data-platform-host", link.dataAttributes["data-platform-host"]],
    ["data-owner", link.dataAttributes["data-owner"]],
    ["data-name", link.dataAttributes["data-name"]],
    ["data-repo-path", link.dataAttributes["data-repo-path"]],
    ["data-number", link.dataAttributes["data-number"]],
    ["data-item-type", link.dataAttributes["data-item-type"]],
    ["data-external-url", link.dataAttributes["data-external-url"]],
  ];
  return attrs
    .filter(([, value]) => value !== undefined)
    .map(([name, value]) => `${name}="${escapeAttribute(value!)}"`)
    .join(" ");
}
