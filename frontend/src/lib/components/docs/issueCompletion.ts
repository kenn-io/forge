import type { Completion, CompletionContext, CompletionResult } from "@codemirror/autocomplete";
import type { IssueSummary, SearchFilters, SearchResponse } from "./docsIssueTypes";

export interface IssueCompletionOptions {
  /** Synchronous snapshot of issues loaded in the current view. */
  getIssues: () => readonly IssueSummary[];
  /**
   * Optional async daemon search. When provided, the source merges
   * results into the synchronous snapshot so the
   * suggestion list can reach issues that aren't in the current view.
   */
  search?: (filters: SearchFilters, daemonKey: string) => Promise<SearchResponse>;
  /** Optional debounce window before issuing a daemon search. Defaults to no delay. */
  debounceMs?: number;
  /**
   * Optional discriminator folded into the daemon-search cache key so callers
   * can scope cached results (e.g. per folder daemon) and avoid reusing one
   * daemon's hits for another.
   */
  cacheKeyPrefix?: () => string;
}

/**
 * Builds a CodeMirror completion source that suggests Kata issues
 * when the user types `#prefix` or `project/#prefix` inside the
 * markdown editor.
 *
 * Local results come from a synchronous getter so the menu always
 * reflects the freshest snapshot without rebuilding the EditorView.
 * When an async `search` is supplied, the source merges
 * daemon hits so the menu reaches beyond the loaded view.
 */
export function buildIssueCompletionSource(options: IssueCompletionOptions | (() => readonly IssueSummary[])) {
  // Accept either the new options object or the legacy bare getter so
  // older callers (and tests) keep working through the signature change.
  const opts: IssueCompletionOptions = typeof options === "function" ? { getIssues: options } : options;
  const debounceMs = opts.debounceMs ?? 0;

  return async function source(context: CompletionContext): Promise<CompletionResult | null> {
    // Read per-invocation: the active folder (and thus its daemon) can change
    // between completion requests on the same source.
    const keyPrefix = opts.cacheKeyPrefix?.() ?? "";
    // Qualified form first: `project/#prefix`. Project segment may
    // contain `:`, `_`, `-`, `.` (e.g. "notes:inbox") to match
    // Kata's project name conventions.
    const qualified = context.matchBefore(/[A-Za-z][\w:.-]*\/#[a-z0-9]*/);
    if (qualified) {
      const idx = qualified.text.indexOf("/#");
      const project = qualified.text.slice(0, idx);
      const prefix = qualified.text.slice(idx + 2);
      const local = opts.getIssues();
      const merged = await mergeWithSearch(local, prefix, opts.search, debounceMs, context, keyPrefix, project, true);
      if (context.aborted) return null;
      const scoped = merged.filter((i) => i.project_name === project);
      const ambiguous = ambiguousShortIDs(merged);
      const options = rankIssues(scoped, prefix).map((issue) => issueCompletion(issue, true, ambiguous));
      return {
        from: qualified.from,
        options,
        // Returning `filter: false` keeps CodeMirror from re-filtering on
        // option.label — our ranker already considered title hits, which
        // CodeMirror's default label-only filter would drop.
        filter: false,
      };
    }

    // Bare form: `#prefix`. Must not be preceded by a word char so
    // we don't trigger inside URL fragments or mid-word references.
    const bare = context.matchBefore(/#[a-z0-9]*/);
    if (!bare) return null;
    if (bare.from > 0) {
      const before = context.state.doc.sliceString(bare.from - 1, bare.from);
      if (/\w/.test(before)) return null;
    }
    // The user explicitly invoked completion at a bare `#` — show all
    // issues. While typing characters after `#`, only show after at
    // least the trigger has fired (matchBefore returns when the prefix
    // is empty too).
    const prefix = bare.text.slice(1);
    const local = opts.getIssues();
    const merged = await mergeWithSearch(local, prefix, opts.search, debounceMs, context, keyPrefix);
    if (context.aborted) return null;
    const ambiguous = ambiguousShortIDs(merged);
    const options = rankIssues(merged, prefix).map((issue) => issueCompletion(issue, false, ambiguous));
    return {
      from: bare.from,
      options,
      filter: false,
    };
  };
}

// Daemon search debounce + dedupe state. The cache key uses both the
// query and the project scope so qualified completions don't reuse the
// untyped result set.
const searchCache = new WeakMap<
  (filters: SearchFilters, daemonKey: string) => Promise<SearchResponse>,
  Map<string, IssueSummary[]>
>();

async function mergeWithSearch(
  local: readonly IssueSummary[],
  prefix: string,
  search: IssueCompletionOptions["search"],
  debounceMs: number,
  context: CompletionContext,
  keyPrefix: string,
  project?: string,
  searchEmptyPrefix = false,
): Promise<IssueSummary[]> {
  if (!search) return [...local];
  // An empty prefix isn't a useful daemon query — it would dump the
  // entire issue list for bare `#`. For qualified `project/#`, allow
  // the daemon search only when the scoped local snapshot has no match.
  if (!prefix && !searchEmptyPrefix) return [...local];

  const cacheKey = `${keyPrefix}::${project ?? ""}::${prefix}`;
  let cache = searchCache.get(search);
  if (!cache) {
    cache = new Map();
    searchCache.set(search, cache);
  }
  const cached = cache.get(cacheKey);
  if (cached) return mergeIssues(local, cached);

  // Local-first: if the loaded snapshot already has at least one match
  // for this prefix (in the same project scope, when given), don't
  // block the menu on a daemon round-trip. A slow or hanging daemon
  // would otherwise prevent any suggestion paint, even though the user
  // is typing a short_id we already have in memory.
  if (localHasMatch(local, prefix, project)) {
    return [...local];
  }

  // Debounce: short pause so we don't fire on every keystroke. Bail
  // immediately if the context aborts during the wait (user kept typing
  // and a newer source call is already in flight).
  if (debounceMs > 0) {
    await sleep(debounceMs);
    if (context.aborted) return [...local];
  }

  try {
    // Always search globally; the qualified form's project filter is
    // applied client-side because we only have a project name here, not
    // a uid, and SearchScope expects a uid.
    const response = await search(
      {
        scope: { kind: "all" },
        status: "all",
        owner: "",
        label: "",
        query: prefix,
      },
      // Same daemon captured for the cache key above, so a daemon switch during
      // the debounce can't search one daemon and cache under another.
      keyPrefix,
    );
    if (context.aborted) return [...local];
    cache.set(cacheKey, response.issues);
    return mergeIssues(local, response.issues);
  } catch {
    // Daemon failure shouldn't take the menu down — fall back to local.
    return [...local];
  }
}

function localHasMatch(local: readonly IssueSummary[], prefix: string, project?: string): boolean {
  const lower = prefix.toLowerCase();
  for (const issue of local) {
    if (project && issue.project_name !== project) continue;
    if (
      issue.short_id.startsWith(lower) ||
      issue.short_id.includes(lower) ||
      issue.title.toLowerCase().includes(lower)
    ) {
      return true;
    }
  }
  return false;
}

function mergeIssues(local: readonly IssueSummary[], remote: readonly IssueSummary[]): IssueSummary[] {
  const seen = new Set<string>();
  const merged: IssueSummary[] = [];
  for (const issue of local) {
    if (seen.has(issue.uid)) continue;
    seen.add(issue.uid);
    merged.push(issue);
  }
  for (const issue of remote) {
    if (seen.has(issue.uid)) continue;
    seen.add(issue.uid);
    merged.push(issue);
  }
  return merged;
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

// Set of short_ids that appear in more than one loaded project. Bare
// completions for these always insert the qualified form so the link
// can't silently resolve to a different project later.
function ambiguousShortIDs(issues: readonly IssueSummary[]): Set<string> {
  const seenIn = new Map<string, Set<string>>();
  for (const issue of issues) {
    let projects = seenIn.get(issue.short_id);
    if (!projects) {
      projects = new Set();
      seenIn.set(issue.short_id, projects);
    }
    projects.add(issue.project_name);
  }
  const out = new Set<string>();
  for (const [shortID, projects] of seenIn) {
    if (projects.size > 1) out.add(shortID);
  }
  return out;
}

function rankIssues(issues: readonly IssueSummary[], prefix: string): IssueSummary[] {
  const lower = prefix.toLowerCase();
  // Prefix-match short_id first, then substring matches in short_id,
  // then substring matches in title. Open issues bubble above closed.
  const prefixHits: IssueSummary[] = [];
  const subHits: IssueSummary[] = [];
  const titleHits: IssueSummary[] = [];
  for (const issue of issues) {
    if (issue.short_id.startsWith(lower)) {
      prefixHits.push(issue);
      continue;
    }
    if (lower && issue.short_id.includes(lower)) {
      subHits.push(issue);
      continue;
    }
    if (lower && issue.title.toLowerCase().includes(lower)) {
      titleHits.push(issue);
    }
  }
  const ordered = [...prefixHits, ...subHits, ...titleHits];
  ordered.sort((a, b) => {
    if (a.status === b.status) return 0;
    return a.status === "open" ? -1 : 1;
  });
  return ordered.slice(0, 50);
}

function issueCompletion(issue: IssueSummary, qualified: boolean, ambiguous: Set<string>): Completion {
  // For bare completions we still write the qualified form when the
  // short_id is shared across projects — otherwise selecting the
  // non-first project produces a ref that opens the wrong issue.
  const writeQualified = qualified || ambiguous.has(issue.short_id);
  const apply = writeQualified ? `${issue.project_name}/#${issue.short_id}` : `#${issue.short_id}`;
  const detail = qualified ? issue.title : `${issue.title}  ·  ${issue.project_name}`;
  return {
    label: apply,
    detail,
    type: issue.status === "open" ? "variable" : "constant",
    apply,
    boost: issue.status === "open" ? 10 : 0,
  };
}
