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
import { parseProviderItemURL, type ResolvableItemReference } from "./item-reference.js";

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

export const resolveItemReferenceEffect = Effect.fn("resolveItemReference")(function* (ref: ResolvableItemReference) {
  const { provider, platformHost, owner, name, repoPath, number, itemType } = ref;
  const result = yield* executeGeneratedApiRequest("resolve item reference", (client, signal) => {
    const routeRef = { provider, platformHost, owner, name, repoPath };
    const itemTypeHint = canonicalProvider(provider) === "gitlab" ? itemType : undefined;
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
  return yield* Effect.sync(() => {
    if ("problem" in result) {
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
      itemType: result.data.item_type === "pr" ? "pr" : "issue",
      provider,
      platformHost,
      owner,
      name,
      repoPath,
      number,
    } satisfies RoutableItemRef;
  });
});

// Resolves an item reference through the repo resolve endpoint and either
// navigates to the internal item route or reports that tracking has changed.
export function resolveItemReference(
  runtime: AppRuntime,
  ref: ResolvableItemReference,
  onNavigate: (ref: RoutableItemRef) => void = (item) => navigate(buildItemRoute(item)),
): AppExecution<void, unknown> {
  return runtime.runCommand(
    resolveItemReferenceEffect(ref).pipe(
      Effect.tap((item) =>
        Effect.sync(() => {
          if (item) onNavigate(item);
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
  getProviderContexts: () => ReadonlyArray<{ provider: string; platformHost?: string | undefined }> = () => [],
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

  function handleClick(e: Event): void {
    if (
      !(e instanceof MouseEvent) ||
      e.defaultPrevented ||
      e.metaKey ||
      e.ctrlKey ||
      e.shiftKey ||
      e.altKey ||
      e.button !== 0
    )
      return;

    const anchor = findAnchor(e.target);
    if (!anchor || anchor.hasAttribute("download")) return;
    if (anchor.target && anchor.target.toLowerCase() !== "_self") return;

    const provider = anchor.dataset.provider;
    const platformHost = anchor.dataset.platformHost;
    const owner = anchor.dataset.owner;
    const name = anchor.dataset.name;
    const repoPath = anchor.dataset.repoPath;
    const numberStr = anchor.dataset.number;
    const itemType =
      anchor.dataset.itemType === "pr" || anchor.dataset.itemType === "issue" ? anchor.dataset.itemType : undefined;
    const externalUrl = anchor.dataset.externalUrl;
    let ref: ResolvableItemReference | null = null;
    if (anchor.classList.contains("item-ref") && provider && owner && name && repoPath && numberStr) {
      ref = {
        provider,
        platformHost,
        owner,
        name,
        repoPath,
        number: parseInt(numberStr, 10),
        itemType,
        externalUrl,
      };
    } else {
      for (const context of [
        ...getProviderContexts(),
        { provider: "github" },
        { provider: "gitlab" },
        { provider: "bitbucket" },
      ]) {
        ref = parseProviderItemURL(anchor.href, context);
        if (ref) break;
      }
    }
    if (!ref) return;

    const tracked = getRepositories().some(
      (repo) =>
        canonicalProvider(repo.Platform) === canonicalProvider(ref.provider) &&
        resolvedPlatformHost(repo.Platform, repo.PlatformHost).toLowerCase() ===
          resolvedPlatformHost(ref.provider, ref.platformHost).toLowerCase() &&
        `${repo.Owner}/${repo.Name}`.toLowerCase() === ref.repoPath.toLowerCase(),
    );
    const browserURL = safeExternalURL(ref.externalUrl);
    if (!tracked && browserURL) {
      // Keep the real click and target; no popup after an asynchronous lookup.
      anchor.href = browserURL;
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

  target.addEventListener("click", handleClick);
  return () => {
    catalogExecution.interrupt();
    execution?.interrupt();
    target.removeEventListener("click", handleClick);
  };
}
