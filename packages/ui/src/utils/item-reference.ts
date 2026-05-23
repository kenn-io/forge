import {
  buildIssueRoute,
  buildRoutedItemRoute,
  type RepositoryRouteRef,
} from "../routes.js";

export type ItemReferenceType = "pr" | "issue";

export type ResolvableItemReference = RepositoryRouteRef & {
  number: number;
  itemType?: ItemReferenceType | undefined;
};

export type ItemReferenceDataAttributes = {
  "data-provider": string;
  "data-owner": string;
  "data-name": string;
  "data-repo-path": string;
  "data-number": string;
  "data-platform-host"?: string | undefined;
};

export type ItemReferenceLink = {
  href: string;
  dataAttributes: ItemReferenceDataAttributes;
};

export function buildItemReferenceHref(ref: ResolvableItemReference): string {
  if (ref.itemType === "pr") {
    return buildRoutedItemRoute({ ...ref, itemType: "pr" });
  }
  if (ref.itemType === "issue") {
    return buildRoutedItemRoute({ ...ref, itemType: "issue" });
  }
  return buildIssueRoute(ref);
}

export function itemReferenceDataAttributes(
  ref: ResolvableItemReference,
): ItemReferenceDataAttributes {
  return {
    "data-provider": ref.provider,
    ...(ref.platformHost && {
      "data-platform-host": ref.platformHost,
    }),
    "data-owner": ref.owner,
    "data-name": ref.name,
    "data-repo-path": ref.repoPath,
    "data-number": ref.number.toString(),
  };
}

export function buildItemReferenceLink(
  ref: ResolvableItemReference,
): ItemReferenceLink {
  return {
    href: buildItemReferenceHref(ref),
    dataAttributes: itemReferenceDataAttributes(ref),
  };
}

function escapeAttribute(value: string): string {
  return value
    .replaceAll("&", "&amp;")
    .replaceAll('"', "&quot;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;");
}

export function itemReferenceAnchorAttributes(
  ref: ResolvableItemReference,
  className = "item-ref",
): string {
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
  ];
  return attrs
    .filter(([, value]) => value !== undefined)
    .map(([name, value]) => `${name}="${escapeAttribute(value!)}"`)
    .join(" ");
}
