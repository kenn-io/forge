import { Effect, Semaphore } from "effect";
import { executeGeneratedApiRequest } from "../api/generated-api.js";
import type { Issue, PullRequest } from "../api/types.js";
import type { AppRuntime } from "../app/runtime.js";

// Match the list API's terms, quoted phrases, and SQL LIKE wildcards.
function searchTerms(query: string): RegExp[] {
  const terms: string[] = [];
  let term = "";
  let quote = "";
  for (const char of query.trim()) {
    if (quote) {
      if (char === quote) quote = "";
      else term += char;
    } else if ((char === '"' || char === "'") && !term) {
      quote = char;
    } else if (/\s/.test(char)) {
      if (term.trim()) terms.push(term.trim());
      term = "";
    } else {
      term += char;
    }
  }
  if (term.trim()) terms.push(term.trim());
  return terms.map(
    (value) =>
      new RegExp(
        value
          .replace(/[.*+?^${}()|[\]\\]/g, "\\$&")
          .replaceAll("%", ".*")
          .replaceAll("_", "."),
        "is",
      ),
  );
}

export function createWorkspaceItemSearchStore(runtime: AppRuntime) {
  let items = $state.raw<{ pulls: PullRequest[]; issues: Issue[] }>();
  let error = $state("");
  const refreshLock = Semaphore.makeUnsafe(1);

  const loadEffect = Effect.gen(function* () {
    yield* Effect.sync(() => {
      error = "";
    });
    const result = yield* Effect.all(
      {
        pulls: executeGeneratedApiRequest("load open workspace pull requests", (client, signal) =>
          client.PullRequestsService.listPulls({ state: "open" }, { signal }),
        ),
        issues: executeGeneratedApiRequest("load open workspace issues", (client, signal) =>
          client.IssuesService.listIssues({ state: "open" }, { signal }),
        ),
      },
      { concurrency: "unbounded" },
    );
    yield* Effect.sync(() => {
      items = result;
    });
  }).pipe(
    Effect.tapError(() =>
      Effect.sync(() => {
        error = "Could not refresh PRs and issues. Type to retry.";
      }),
    ),
  );
  const refreshEffect = loadEffect.pipe(refreshLock.withPermits(1));

  function ensureLoaded(): void {
    if (items !== undefined && !error) return;
    runtime.runCommand(loadEffect.pipe(refreshLock.withPermitsIfAvailable(1), Effect.asVoid), {
      operation: "load workspace search items",
      safeContext: {},
      onFailure: () => {},
    });
  }

  function search(query: string): { pulls: PullRequest[]; issues: Issue[] } {
    const terms = searchTerms(query);
    const matches = (item: PullRequest | Issue) => {
      const fields = [
        `#${item.Number} ${item.Title}`,
        item.Author,
        item.repo.repo_path,
        item.repo.owner,
        item.repo.name,
        ...(item.labels ?? []).map((label) => label.name),
      ];
      return terms.every((term) => fields.some((field) => term.test(field)));
    };
    const exactPRNumber = /^#?0\d+$/.test(query.trim()) ? Number(query.trim().replace(/^#/, "")) : undefined;
    return {
      pulls: (items?.pulls ?? [])
        .filter((item) => (exactPRNumber === undefined ? matches(item) : item.Number === exactPRNumber))
        .slice(0, 30),
      issues: (items?.issues ?? []).filter(matches).slice(0, 30),
    };
  }

  return {
    ensureLoaded,
    refreshEffect,
    search,
    isLoading: () => items === undefined && !error,
    getError: () => error,
  };
}

export type WorkspaceItemSearchStore = ReturnType<typeof createWorkspaceItemSearchStore>;
