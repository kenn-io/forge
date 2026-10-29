import { Effect } from "effect";

import type { ApiProblemError, TransientTransportError } from "../../api/effect-errors.js";
import { executeGeneratedApiRequest } from "../../api/generated-api.js";
import type { Issue, PullRequest } from "../../api/types.js";
import type { ParsedQuery } from "./palette-search.svelte.js";

// "text" matches loaded titles locally; "meaning" asks the server to rank
// pull requests and issues by how closely their titles, descriptions, and
// comments match the query.
export type PaletteSearchMode = "text" | "meaning";

export const SEARCH_MODE_KEY = "kenn-forge-palette-search-mode";

// Shorter queries carry too little meaning to rank; they keep title matching.
export const SEMANTIC_MIN_QUERY_LENGTH = 3;
export const SEMANTIC_RESULT_LIMIT = 10;

export type SemanticQuery = { scope: "all" | "pr" | "issue"; query: string };

export interface SemanticMatches {
  pulls: PullRequest[];
  issues: Issue[];
}

export function readSearchMode(): PaletteSearchMode {
  try {
    return localStorage.getItem(SEARCH_MODE_KEY) === "meaning" ? "meaning" : "text";
  } catch {
    return "text";
  }
}

export function writeSearchMode(mode: PaletteSearchMode): void {
  try {
    localStorage.setItem(SEARCH_MODE_KEY, mode);
  } catch {
    // localStorage unavailable; the mode lasts until reload.
  }
}

// semanticQueryFor returns the query to rank by meaning, or null when the
// palette should keep matching titles locally.
export function semanticQueryFor(mode: PaletteSearchMode, parsed: ParsedQuery): SemanticQuery | null {
  if (mode !== "meaning") return null;
  if (parsed.scope !== "all" && parsed.scope !== "pr" && parsed.scope !== "issue") return null;
  if (parsed.query.length < SEMANTIC_MIN_QUERY_LENGTH) return null;
  return { scope: parsed.scope, query: parsed.query };
}

// rankByMeaning reads the best matches in every state, since related or
// duplicate work is often already closed.
export const rankByMeaning = Effect.fn("Palette.rankByMeaning")(function* (query: SemanticQuery) {
  const params = { q: query.query, semantic: true, state: "all", limit: SEMANTIC_RESULT_LIMIT };
  const [pulls, issues] = yield* Effect.all(
    [
      query.scope === "issue"
        ? Effect.succeed<PullRequest[]>([])
        : executeGeneratedApiRequest("rank pull requests by meaning", (client, signal) =>
            client.PullRequestsService.listPulls(params, { signal }),
          ),
      query.scope === "pr"
        ? Effect.succeed<Issue[]>([])
        : executeGeneratedApiRequest("rank issues by meaning", (client, signal) =>
            client.IssuesService.listIssues(params, { signal }),
          ),
    ],
    { concurrency: "unbounded" },
  );
  return { pulls, issues } satisfies SemanticMatches;
});

// semanticFailureMessage shows the server's explanation, which names the
// missing [embeddings] setup when semantic search is not configured.
export function semanticFailureMessage(failure: ApiProblemError | TransientTransportError): string {
  if (failure._tag === "ApiProblemError") {
    return failure.problem.detail ?? failure.problem.title ?? "Semantic search failed";
  }
  return "Could not reach Forge";
}
