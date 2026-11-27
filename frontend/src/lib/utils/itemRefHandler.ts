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
import type { UntrackedOpenInputBody } from "../api/generated/index.js";
import type { RepoCatalog } from "../api/types.js";
import type { AppExecution, AppRuntime } from "../app/runtime.js";
import { navigate, buildItemRoute, getURLChangeCount, type RoutableItemRef } from "../stores/router.svelte.js";
import { showFlash } from "../stores/flash.svelte.js";
import { parseKnownProviderItemURL, type ProviderContext, type ResolvableItemReference } from "./item-reference.js";
import { navigateToURL } from "./pageNavigation.js";

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

export function findItemRefAnchor(target: EventTarget | null): HTMLAnchorElement | null {
  let el = target instanceof Element ? target : null;
  while (el) {
    if (el instanceof HTMLAnchorElement) {
      return el;
    }
    el = el.parentElement;
  }
  return null;
}

export function resolveAnchorItemReference(
  anchor: HTMLAnchorElement,
  providerContexts: ReadonlyArray<ProviderContext> = [],
): ResolvableItemReference | null {
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
  return parseKnownProviderItemURL(anchor.href, providerContexts);
}

export function isTrackedItemReference(ref: ResolvableItemReference, repos: ReadonlyArray<RepoCatalog>): boolean {
  const provider = canonicalProvider(ref.provider);
  const host = resolvedPlatformHost(ref.provider, ref.platformHost).toLowerCase();
  const repoPath = ref.repoPath.toLowerCase();
  return repos.some(
    (repo) =>
      canonicalProvider(repo.Platform) === provider &&
      resolvedPlatformHost(repo.Platform, repo.PlatformHost).toLowerCase() === host &&
      `${repo.Owner}/${repo.Name}`.toLowerCase() === repoPath,
  );
}

// A reference resolves to an in-app item, to its provider page when Forge has
// not synced the item, or to a message saying why it cannot open. Callers
// decide whether the result is still current before acting on it.
export type ItemReferenceResolution =
  | { readonly kind: "item"; readonly item: RoutableItemRef }
  | { readonly kind: "providerPage"; readonly url: string }
  | { readonly kind: "unresolved"; readonly message: string; readonly reason: "not-found" | "not-tracked" | "error" };

export const resolveItemReferenceEffect = Effect.fn("resolveItemReference")(
  function* (ref: ResolvableItemReference) {
    const { provider, platformHost, owner, name, repoPath, number, itemType } = ref;
    const result = yield* executeGeneratedApiRequest("resolve item reference", (client, signal) => {
      const routeRef = { provider, platformHost, owner, name, repoPath };
      const query = itemType === undefined ? undefined : { item_type: itemType };
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
    return yield* Effect.sync((): ItemReferenceResolution => {
      if ("problem" in result) {
        const providerURL = safeExternalURL(ref.externalUrl);
        if (result.problem.status === 404 && providerURL) {
          return { kind: "providerPage", url: providerURL };
        }
        return {
          kind: "unresolved",
          reason: result.problem.status === 404 ? "not-found" : "error",
          message:
            result.problem.status === 404
              ? `Item ${owner}/${name}#${number} not found.`
              : `Could not open ${owner}/${name}#${number}. Try again.`,
        };
      }

      if (!result.data.repo_tracked) {
        return {
          kind: "unresolved",
          reason: "not-tracked",
          message: `${owner}/${name} is not tracked. Add it in Settings to navigate here.`,
        };
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
  },
  (effect, ref) =>
    effect.pipe(
      Effect.catchTag("TransientTransportError", () =>
        Effect.succeed<ItemReferenceResolution>({
          kind: "unresolved",
          reason: "error",
          message: `Could not reach Forge to open ${ref.owner}/${ref.name}#${ref.number}. Try again.`,
        }),
      ),
    ),
);

// Resolves an item reference through the repo resolve endpoint and either
// navigates to the internal item route or reports that tracking has changed.
export function resolveItemReference(
  runtime: AppRuntime,
  ref: ResolvableItemReference,
  onNavigate: (ref: RoutableItemRef) => void = (item) => navigate(buildItemRoute(item)),
): AppExecution<void, unknown> {
  const urlChangesAtClick = getURLChangeCount();
  // The user has moved on once the URL changed, even back to the same value.
  const stillCurrent = () => getURLChangeCount() === urlChangesAtClick;
  return runtime.runCommand(
    resolveItemReferenceEffect(ref).pipe(
      Effect.tap((resolution) =>
        Effect.sync(() => {
          if (!stillCurrent()) return;
          if (resolution.kind === "item") onNavigate(resolution.item);
          // The click was cancelled before the lookup, so the provider page
          // opens in this tab; browsers block a tab opened after an await.
          else if (resolution.kind === "providerPage") navigateToURL(resolution.url);
          else showFlash(resolution.message, { tone: "danger" });
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
        if (stillCurrent())
          showFlash(`Could not open ${ref.owner}/${ref.name}#${ref.number}. Try again.`, { tone: "danger" });
      },
    },
  );
}

const recordedOpenEvents = new WeakSet<Event>();
const navigateHandlers = new WeakMap<EventTarget, Array<(ref: RoutableItemRef) => void>>();

// Invokes the nearest initItemRefHandler navigation callback enclosing anchor
// so resolved previews preserve scoped routing (such as worklist selection)
// for both visible and hidden tracked repositories.
export function openItemReferenceFromAnchor(anchor: HTMLAnchorElement | null, item: RoutableItemRef): boolean {
  if (!anchor?.isConnected) return false;
  let node: Node | null = anchor;
  while (node) {
    const handlers = navigateHandlers.get(node);
    const handler = handlers?.[handlers.length - 1];
    if (handler) {
      handler(item);
      return true;
    }
    node = node.parentNode;
  }
  return false;
}

export function recordUntrackedRepoOpen(runtime: AppRuntime, body: UntrackedOpenInputBody): void {
  runtime.runCommand(
    executeGeneratedApiRequest("record untracked repository open", (client, signal) =>
      client.RepositoriesService.recordUntrackedRepoOpen(body, { signal, keepalive: true }),
    ),
    { operation: "record untracked repository open", safeContext: {}, onFailure: () => {} },
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

  function handleClick(e: Event): void {
    if (!(e instanceof MouseEvent) || e.defaultPrevented) return;
    const anchor = findItemRefAnchor(e.target);
    if (!anchor || anchor.hasAttribute("download")) return;
    const ref = resolveAnchorItemReference(anchor, getProviderContexts());
    if (!ref) return;

    // Every way of opening an untracked item, including new-tab clicks and the
    // context menu's open and copy entries, uses the provider URL; Forge has no
    // page for it.
    const browserURL = isTrackedItemReference(ref, getRepositories()) ? null : safeExternalURL(ref.externalUrl);
    if (browserURL) anchor.href = browserURL;
    if (browserURL && !recordedOpenEvents.has(e) && (e.type === "click" || (e.type === "auxclick" && e.button === 1))) {
      recordedOpenEvents.add(e);
      recordUntrackedRepoOpen(runtime, {
        provider: ref.provider,
        host: ref.platformHost ?? "",
        owner: ref.owner,
        name: ref.name,
        ...(ref.itemType ? { item_type: ref.itemType } : {}),
        number: ref.number,
      });
    }

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

  if (onNavigate) {
    const existing = navigateHandlers.get(target);
    if (existing) existing.push(onNavigate);
    else navigateHandlers.set(target, [onNavigate]);
  }

  const openEvents = ["click", "auxclick", "contextmenu"] as const;
  for (const type of openEvents) target.addEventListener(type, handleClick);
  return () => {
    if (onNavigate) {
      const existing = navigateHandlers.get(target);
      if (existing) {
        const idx = existing.lastIndexOf(onNavigate);
        if (idx !== -1) existing.splice(idx, 1);
        if (existing.length === 0) navigateHandlers.delete(target);
      }
    }
    catalogExecution.interrupt();
    execution?.interrupt();
    for (const type of openEvents) target.removeEventListener(type, handleClick);
  };
}
