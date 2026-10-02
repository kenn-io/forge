import { Effect } from "effect";
import { GeneratedApi } from "../api/generated-api.js";
import {
  canonicalProvider,
  resolvedPlatformHost,
  providerRouteParams,
  providerHostRouteParams,
  providerUsesHostRoute,
} from "../api/provider-routes.js";
import { RepositoryReads } from "../api/repository-reads.js";
import type { RepoCatalog } from "../api/types.js";
import { GeneratedProblemResponse } from "../api/runtime.js";
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

function resolveAndNavigate(
  ref: ResolvableItemReference,
  onNavigate: (ref: RoutableItemRef) => void,
): Effect.Effect<void, unknown, GeneratedApi> {
  const { provider, platformHost, owner, name, repoPath, number, itemType } = ref;
  return Effect.gen(function* () {
    const api = yield* GeneratedApi;
    const result = yield* Effect.tryPromise({
      try: (signal) => {
        const routeRef = { provider, platformHost, owner, name, repoPath };
        const itemTypeHint = canonicalProvider(provider) === "gitlab" ? itemType : undefined;
        const query = itemTypeHint === undefined ? undefined : { item_type: itemTypeHint };
        const request = providerUsesHostRoute(routeRef)
          ? api.client.RepositoriesService.resolveRepoItemOnHost(
              { ...providerHostRouteParams(routeRef), number },
              query,
              {
                signal,
              },
            )
          : api.client.RepositoriesService.resolveRepoItem({ ...providerRouteParams(routeRef), number }, query, {
              signal,
            });
        return request.then(
          (data) => ({ data }) as const,
          (cause: unknown) => {
            if (cause instanceof GeneratedProblemResponse) return { problem: cause.problem } as const;
            throw cause;
          },
        );
      },
      catch: (cause) => cause,
    });
    yield* Effect.sync(() => {
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

      onNavigate({
        itemType: result.data.item_type === "pr" ? "pr" : "issue",
        provider,
        platformHost,
        owner,
        name,
        repoPath,
        number,
      });
    });
  });
}

// Resolves an item reference through the repo resolve endpoint and either
// navigates to the internal item route or reports that tracking has changed.
export function resolveItemReference(
  runtime: AppRuntime,
  ref: ResolvableItemReference,
  onNavigate: (ref: RoutableItemRef) => void = (item) => navigate(buildItemRoute(item)),
): AppExecution<void, unknown> {
  return runtime.runCommand(resolveAndNavigate(ref, onNavigate), {
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
  });
}

export function initItemRefHandler(
  runtime: AppRuntime,
  onNavigate?: (ref: RoutableItemRef) => void,
  target: HTMLElement | Document = document,
  getProviderContexts: () => ReadonlyArray<{ provider: string; platformHost?: string | undefined }> = () => [],
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
    execution = resolveItemReference(runtime, ref, onNavigate);
  }

  target.addEventListener("click", handleClick);
  return () => {
    catalogExecution.interrupt();
    execution?.interrupt();
    target.removeEventListener("click", handleClick);
  };
}
