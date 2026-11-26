import { Effect } from "effect";
import { executeGeneratedApiRequest } from "../api/generated-api.js";
import { canonicalProvider, resolvedPlatformHost } from "../api/provider-routes.js";
import { RepositoryReads } from "../api/repository-reads.js";
import type { Issue, PullRequest } from "../api/types.js";
import type { ItemReferenceType, ResolvableItemReference } from "../utils/item-reference.js";
import { isTrackedItemReference, resolveItemReferenceEffect } from "../utils/itemRefHandler.js";

export type ItemPreview =
  | { readonly kind: "pr"; readonly pull: PullRequest }
  | { readonly kind: "issue"; readonly issue: Issue };

export type ItemPreviewOutcome =
  | { readonly status: "found"; readonly preview: ItemPreview }
  | { readonly status: "untracked" }
  | { readonly status: "not-found" }
  | { readonly status: "error"; readonly message: string };

interface CachedPreviewEntry {
  readonly outcome: Extract<ItemPreviewOutcome, { status: "found" | "not-found" }>;
  readonly expiresAt: number;
}

export interface ItemPreviewCacheOptions {
  readonly ttlMs?: number;
  readonly notFoundTtlMs?: number;
  readonly maxEntries?: number;
  readonly now?: () => number;
}

const DEFAULT_TTL_MS = 30_000;
const DEFAULT_NOT_FOUND_TTL_MS = 10_000;
const DEFAULT_MAX_ENTRIES = 100;

function usesSeparateItemNamespaces(provider: string): boolean {
  const canonical = canonicalProvider(provider);
  return canonical === "gitlab" || canonical === "bitbucket";
}

export function itemPreviewCacheKey(ref: ResolvableItemReference, itemType: ItemReferenceType | "any"): string {
  return [
    canonicalProvider(ref.provider),
    resolvedPlatformHost(ref.provider, ref.platformHost).toLowerCase(),
    ref.repoPath.toLowerCase(),
    itemType,
    ref.number.toString(),
  ].join("\0");
}

export class ItemPreviewCache {
  readonly #entries = new Map<string, CachedPreviewEntry>();
  readonly #ttlMs: number;
  readonly #notFoundTtlMs: number;
  readonly #maxEntries: number;
  readonly #now: () => number;

  constructor(options: ItemPreviewCacheOptions = {}) {
    this.#ttlMs = options.ttlMs ?? DEFAULT_TTL_MS;
    this.#notFoundTtlMs = options.notFoundTtlMs ?? DEFAULT_NOT_FOUND_TTL_MS;
    this.#maxEntries = options.maxEntries ?? DEFAULT_MAX_ENTRIES;
    this.#now = options.now ?? (() => Date.now());
  }

  get(ref: ResolvableItemReference): Extract<ItemPreviewOutcome, { status: "found" | "not-found" }> | undefined {
    const keys = this.#lookupKeys(ref);
    const now = this.#now();
    for (const key of keys) {
      const entry = this.#entries.get(key);
      if (!entry) continue;
      if (entry.expiresAt <= now) {
        this.#entries.delete(key);
        continue;
      }
      // Refresh insertion order for LRU eviction.
      this.#entries.delete(key);
      this.#entries.set(key, entry);
      return entry.outcome;
    }
    return undefined;
  }

  set(ref: ResolvableItemReference, outcome: Extract<ItemPreviewOutcome, { status: "found" | "not-found" }>): void {
    const ttl = outcome.status === "found" ? this.#ttlMs : this.#notFoundTtlMs;
    const entry: CachedPreviewEntry = { outcome, expiresAt: this.#now() + ttl };
    const keys = this.#writeKeys(ref, outcome);
    for (const key of keys) {
      this.#entries.delete(key);
      this.#entries.set(key, entry);
    }
    while (this.#entries.size > this.#maxEntries) {
      const oldest = this.#entries.keys().next().value;
      if (oldest === undefined) break;
      this.#entries.delete(oldest);
    }
  }

  clear(): void {
    this.#entries.clear();
  }

  #lookupKeys(ref: ResolvableItemReference): string[] {
    if (ref.itemType) {
      const exact = itemPreviewCacheKey(ref, ref.itemType);
      return usesSeparateItemNamespaces(ref.provider) ? [exact] : [exact, itemPreviewCacheKey(ref, "any")];
    }
    if (usesSeparateItemNamespaces(ref.provider)) {
      return [itemPreviewCacheKey(ref, "any")];
    }
    return [itemPreviewCacheKey(ref, "any"), itemPreviewCacheKey(ref, "pr"), itemPreviewCacheKey(ref, "issue")];
  }

  #writeKeys(
    ref: ResolvableItemReference,
    outcome: Extract<ItemPreviewOutcome, { status: "found" | "not-found" }>,
  ): string[] {
    if (outcome.status === "found") {
      const concrete = itemPreviewCacheKey(ref, outcome.preview.kind);
      if (usesSeparateItemNamespaces(ref.provider)) {
        return ref.itemType === undefined ? [concrete, itemPreviewCacheKey(ref, "any")] : [concrete];
      }
      return [concrete, itemPreviewCacheKey(ref, "any")];
    }
    return [itemPreviewCacheKey(ref, ref.itemType ?? "any")];
  }
}

function repoFilterParam(ref: ResolvableItemReference): string {
  const provider = canonicalProvider(ref.provider);
  const host = resolvedPlatformHost(ref.provider, ref.platformHost);
  return `${provider}|${host}/${ref.repoPath}`;
}

function matchesItemIdentity(
  candidate: Pick<PullRequest | Issue, "Number" | "repo" | "platform_host" | "repo_owner" | "repo_name">,
  ref: ResolvableItemReference,
): boolean {
  if (candidate.Number !== ref.number) return false;
  const candidateProvider = canonicalProvider(candidate.repo.provider);
  const refProvider = canonicalProvider(ref.provider);
  if (candidateProvider !== refProvider) return false;
  const candidateHost = resolvedPlatformHost(
    candidate.repo.provider,
    candidate.repo.platform_host || candidate.platform_host,
  ).toLowerCase();
  const refHost = resolvedPlatformHost(ref.provider, ref.platformHost).toLowerCase();
  if (candidateHost !== refHost) return false;
  const candidatePath = (candidate.repo.repo_path || `${candidate.repo_owner}/${candidate.repo_name}`).toLowerCase();
  return candidatePath === ref.repoPath.toLowerCase();
}

const fetchPullPreview = (repo: string, ref: ResolvableItemReference) =>
  executeGeneratedApiRequest("load pull request preview", (client, signal) =>
    client.PullRequestsService.listPulls({ repo, numbers: [ref.number], state: "all", limit: 1 }, { signal }),
  ).pipe(Effect.map((pulls) => pulls.find((pull) => matchesItemIdentity(pull, ref))));

const fetchIssuePreview = (repo: string, ref: ResolvableItemReference) =>
  executeGeneratedApiRequest("load issue preview", (client, signal) =>
    client.IssuesService.listIssues({ repo, numbers: [ref.number], state: "all", limit: 1 }, { signal }),
  ).pipe(Effect.map((issues) => issues.find((issue) => matchesItemIdentity(issue, ref))));

function readLocalPreview(repo: string, ref: ResolvableItemReference, kind: ItemReferenceType | undefined) {
  if (kind === "pr") {
    return fetchPullPreview(repo, ref).pipe(
      Effect.map((pull): ItemPreview | undefined => (pull ? { kind: "pr", pull } : undefined)),
    );
  }
  if (kind === "issue") {
    return fetchIssuePreview(repo, ref).pipe(
      Effect.map((issue): ItemPreview | undefined => (issue ? { kind: "issue", issue } : undefined)),
    );
  }
  return Effect.all([fetchPullPreview(repo, ref), fetchIssuePreview(repo, ref)], { concurrency: 2 }).pipe(
    Effect.map(([pull, issue]): ItemPreview | undefined => {
      if (pull) return { kind: "pr", pull };
      if (issue) return { kind: "issue", issue };
      return undefined;
    }),
  );
}

export const loadItemPreviewEffect = Effect.fn("loadItemPreview")(
  function* (ref: ResolvableItemReference, cache?: ItemPreviewCache) {
    const cached = cache?.get(ref);
    if (cached !== undefined) {
      return cached;
    }

    const repoReads = yield* RepositoryReads;
    const repos = repoReads.snapshot ?? (yield* repoReads.refresh);
    if (!isTrackedItemReference(ref, repos)) {
      return { status: "untracked" } satisfies ItemPreviewOutcome;
    }

    const repo = repoFilterParam(ref);
    const local = yield* readLocalPreview(repo, ref, ref.itemType);
    if (local !== undefined) {
      const outcome = { status: "found", preview: local } as const;
      cache?.set(ref, outcome);
      return outcome;
    }

    // Tracked repository, but the item is not yet synced locally (for example
    // an older closed or merged PR). Ask Forge to resolve and sync it on demand.
    const resolution = yield* resolveItemReferenceEffect(ref);
    if (resolution.kind === "providerPage") {
      const outcome = { status: "not-found" } as const;
      cache?.set(ref, outcome);
      return outcome;
    }
    if (resolution.kind === "unresolved") {
      if (resolution.reason === "not-tracked") {
        return { status: "untracked" } satisfies ItemPreviewOutcome;
      }
      if (resolution.reason === "not-found") {
        const outcome = { status: "not-found" } as const;
        cache?.set(ref, outcome);
        return outcome;
      }
      return { status: "error", message: "Could not load preview." } satisfies ItemPreviewOutcome;
    }

    const synced = yield* readLocalPreview(repo, ref, resolution.item.itemType);
    if (synced !== undefined) {
      const outcome = { status: "found", preview: synced } as const;
      cache?.set(ref, outcome);
      return outcome;
    }
    const notFound = { status: "not-found" } as const;
    cache?.set(ref, notFound);
    return notFound;
  },
  (effect) =>
    effect.pipe(
      Effect.catchTag("ApiProblemError", () =>
        Effect.succeed<ItemPreviewOutcome>({ status: "error", message: "Could not load preview." }),
      ),
      Effect.catchTag("TransientTransportError", () =>
        Effect.succeed<ItemPreviewOutcome>({ status: "error", message: "Could not load preview." }),
      ),
    ),
);
