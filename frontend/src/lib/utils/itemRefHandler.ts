import { Effect } from "effect";
import { executeGeneratedApiRequest } from "../api/generated-api.js";
import {
  canonicalProvider,
  resolvedPlatformHost,
  providerRouteParams,
  providerHostRouteParams,
  providerUsesHostRoute,
} from "../api/provider-routes.js";
import { RepositoryReads } from "../api/repository-reads.js";
import type { RepoCatalog } from "../api/types.js";
import type { AppExecution, AppRuntime } from "../app/runtime.js";
import { navigate, buildItemRoute, type RoutableItemRef } from "../stores/router.svelte.js";
import { showFlash } from "../stores/flash.svelte.js";
import { parseKnownProviderItemURL, type ProviderContext, type ResolvableItemReference } from "./item-reference.js";
import { currentInAppPath, navigateToURL } from "./pageNavigation.js";

function safeExternalURL(raw: string | undefined): string | null {
  if (!raw) return null;
  try {
    const url = new URL(raw);
    if (url.protocol === "http:" || url.protocol === "https:") {
      return url.href;
    }
  } catch {
    return null;
  }
  return null;
}

function findAnchor(target: EventTarget | null): HTMLAnchorElement | null {
  let el = target instanceof Element ? target : null;
  while (el) {
    if (el instanceof HTMLAnchorElement) {
      return el;
    }
    el = el.parentElement;
  }
  return null;
}

// A reference resolves to an in-app item, or to its provider page when Forge has
// not synced the item. Callers decide whether the result is still current
// before acting on either.
export type ItemReferenceResolution =
  | { readonly kind: "item"; readonly item: RoutableItemRef }
  | { readonly kind: "providerPage"; readonly url: string };

export const resolveItemReferenceEffect = Effect.fn("resolveItemReference")(function* (ref: ResolvableItemReference) {
  const { provider, platformHost, owner, name, repoPath, number, itemType } = ref;
  const result = yield* executeGeneratedApiRequest("resolve item reference", (client, signal) => {
    const routeRef = { provider, platformHost, owner, name, repoPath };
    // GitLab and Bitbucket number issues and pull requests separately.
    const separateNumbers = ["gitlab", "bitbucket"].includes(canonicalProvider(provider));
    const itemTypeHint = separateNumbers ? itemType : undefined;
    const query = itemTypeHint === undefined ? undefined : { item_type: itemTypeHint };
    return providerUsesHostRoute(routeRef)
      ? client.RepositoriesService.resolveRepoItemOnHost({ ...providerHostRouteParams(routeRef), number }, query, {
          signal,
        })
      : client.RepositoriesService.resolveRepoItem({ ...providerRouteParams(routeRef), number }, query, {
          signal,
        });
  }).pipe(
    Effect.map((data) => ({ data }) as const),
    Effect.catchTag("ApiProblemError", ({ problem }) => Effect.succeed({ problem } as const)),
  );
  return yield* Effect.sync((): ItemReferenceResolution | undefined => {
    if ("problem" in result) {
      const providerURL = safeExternalURL(ref.externalUrl);
      if (result.problem.status === 404 && providerURL) {
        return { kind: "providerPage", url: providerURL };
      }
      if (result.problem.status === 404) {
        showFlash(`Item ${owner}/${name}#${number} not found.`, { tone: "danger" });
      } else {
        showFlash(`Failed to resolve ${owner}/${name}#${number}. Try again later.`, { tone: "danger" });
      }
      return;
    }

    if (!result.data.repo_tracked) {
      showFlash(`${owner}/${name} is not tracked. Add it in Settings to navigate here.`, { tone: "danger" });
      return;
    }

    return {
      kind: "item",
      item: {
        itemType: result.data.item_type === "pr" ? "pr" : "issue",
        provider,
        platformHost,
        owner,
        name,
        repoPath,
        number,
      },
    };
  });
});

// Resolves an item reference through the repo resolve endpoint and either
// navigates to the internal item route or reports that tracking has changed.
export function resolveItemReference(
  runtime: AppRuntime,
  ref: ResolvableItemReference,
  onNavigate: (ref: RoutableItemRef) => void = (item) => navigate(buildItemRoute(item)),
): AppExecution<void, unknown> {
  const originPath = currentInAppPath();
  return runtime.runCommand(
    resolveItemReferenceEffect(ref).pipe(
      Effect.tap((resolution) =>
        Effect.sync(() => {
          if (resolution?.kind === "item") onNavigate(resolution.item);
          // The click was cancelled before the lookup, so the provider page
          // opens in this tab; browsers block a tab opened after an await.
          // Skip it once the user has moved to another page or selection.
          else if (resolution?.kind === "providerPage" && currentInAppPath() === originPath)
            navigateToURL(resolution.url);
        }),
      ),
      Effect.asVoid,
    ),
    {
      operation: "resolve item reference",
      safeContext: {
        provider: ref.provider,
        platformHost: ref.platformHost ?? "",
        owner: ref.owner,
        name: ref.name,
        number: ref.number.toString(),
      },
      onFailure: () => {
        showFlash("Failed to resolve item reference. Check your connection.", { tone: "danger" });
      },
    },
  );
}

export function initItemRefHandler(
  runtime: AppRuntime,
  onNavigate?: (ref: RoutableItemRef) => void,
  target: HTMLElement | Document = document,
  getProviderContexts: () => ReadonlyArray<ProviderContext> = () => [],
  resolveReference: (ref: ResolvableItemReference) => AppExecution<void, unknown> | undefined = (ref) =>
    resolveItemReference(runtime, ref, onNavigate),
): () => void {
  let execution: AppExecution<void, unknown> | null = null;
  let getRepositories: () => readonly RepoCatalog[] = () => [];
  const catalogExecution = runtime.runCommand(
    Effect.gen(function* () {
      const reads = yield* RepositoryReads;
      getRepositories = () => reads.snapshot ?? [];
      if (reads.snapshot === undefined) yield* reads.refresh;
    }),
    {
      operation: "load repositories for item links",
      safeContext: {},
      onFailure: () => {}, // Until the catalog is available, links keep their browser behavior.
    },
  );

  function anchorReference(anchor: HTMLAnchorElement): ResolvableItemReference | null {
    const provider = anchor.dataset.provider;
    const owner = anchor.dataset.owner;
    const name = anchor.dataset.name;
    const repoPath = anchor.dataset.repoPath;
    const numberStr = anchor.dataset.number;
    if (anchor.classList.contains("item-ref") && provider && owner && name && repoPath && numberStr) {
      const itemType = anchor.dataset.itemType;
      return {
        provider,
        platformHost: anchor.dataset.platformHost,
        owner,
        name,
        repoPath,
        number: parseInt(numberStr, 10),
        itemType: itemType === "pr" || itemType === "issue" ? itemType : undefined,
        externalUrl: anchor.dataset.externalUrl,
      };
    }
    return parseKnownProviderItemURL(anchor.href, getProviderContexts());
  }

  function isTracked(ref: ResolvableItemReference): boolean {
    return getRepositories().some(
      (repo) =>
        canonicalProvider(repo.Platform) === canonicalProvider(ref.provider) &&
        resolvedPlatformHost(repo.Platform, repo.PlatformHost).toLowerCase() ===
          resolvedPlatformHost(ref.provider, ref.platformHost).toLowerCase() &&
        `${repo.Owner}/${repo.Name}`.toLowerCase() === ref.repoPath.toLowerCase(),
    );
  }

  function handleClick(e: Event): void {
    if (!(e instanceof MouseEvent) || e.defaultPrevented) return;
    const anchor = findAnchor(e.target);
    if (!anchor || anchor.hasAttribute("download")) return;
    const ref = anchorReference(anchor);
    if (!ref) return;

    // Every way of opening an untracked item, including new-tab clicks and the
    // context menu's open and copy entries, uses the provider URL; Forge has no
    // page for it.
    const browserURL = isTracked(ref) ? null : safeExternalURL(ref.externalUrl);
    if (browserURL) anchor.href = browserURL;

    const plainClick = e.type === "click" && e.button === 0 && !e.metaKey && !e.ctrlKey && !e.shiftKey && !e.altKey;
    if (!plainClick || (anchor.target && anchor.target.toLowerCase() !== "_self")) return;

    if (browserURL) {
      // Keep the real click and target; no popup after an asynchronous lookup.
      if (anchor.classList.contains("item-ref") && !anchor.target) {
        anchor.target = "_blank";
        anchor.relList.add("noopener", "noreferrer");
      }
      return;
    }

    e.preventDefault();
    execution?.interrupt();
    execution = resolveReference(ref) ?? null;
  }

  const openEvents = ["click", "auxclick", "contextmenu"] as const;
  for (const type of openEvents) target.addEventListener(type, handleClick);
  return () => {
    catalogExecution.interrupt();
    execution?.interrupt();
    for (const type of openEvents) target.removeEventListener(type, handleClick);
  };
}
