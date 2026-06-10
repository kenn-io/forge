import type { Completion, CompletionContext, CompletionResult } from "@codemirror/autocomplete";
import type { IssueSummary } from "./docsIssueTypes";

/**
 * Builds a CodeMirror completion source for `@name` mentions. The
 * caller supplies a getter that returns the deduped list of names
 * the user can tag — typically derived from author/owner strings on
 * loaded issues. Suggestions are filtered by the typed prefix and
 * inserted verbatim as `@name`.
 */
export function buildMentionCompletionSource(getNames: () => readonly string[]) {
  return function source(context: CompletionContext): CompletionResult | null {
    const match = context.matchBefore(/@[A-Za-z0-9._-]*/);
    if (!match) return null;
    if (match.from > 0) {
      const before = context.state.doc.sliceString(match.from - 1, match.from);
      if (/\w/.test(before)) return null;
    }
    const prefix = match.text.slice(1).toLowerCase();
    const names = getNames();
    if (names.length === 0) return null;
    const options: Completion[] = rankNames(names, prefix).map((name) => ({
      label: `@${name}`,
      apply: `@${name}`,
      type: "text",
    }));
    return {
      from: match.from,
      options,
      validFor: /^@[A-Za-z0-9._-]*$/,
    };
  };
}

function rankNames(names: readonly string[], prefix: string): string[] {
  if (!prefix) return [...names].sort().slice(0, 50);
  const lower = prefix.toLowerCase();
  const prefixHits: string[] = [];
  const subHits: string[] = [];
  for (const name of names) {
    const lname = name.toLowerCase();
    if (lname.startsWith(lower)) prefixHits.push(name);
    else if (lname.includes(lower)) subHits.push(name);
  }
  prefixHits.sort();
  subHits.sort();
  return [...prefixHits, ...subHits].slice(0, 50);
}

/**
 * Collects distinct, mention-able names from a set of loaded issues:
 * authors and owners that look like plain handles (no spaces, no
 * `@` prefix already). Sorted alphabetically.
 */
export function collectMentionNames(issues: readonly IssueSummary[]): string[] {
  const set = new Set<string>();
  for (const issue of issues) {
    addHandle(set, issue.author);
    addHandle(set, issue.owner);
  }
  return [...set].sort((a, b) => a.localeCompare(b));
}

// Mention handle grammar shared with the markdown tokenizer/renderer
// (see MENTION_RE in docsMarkdown.ts) — strings like "agent:planner"
// would otherwise be suggested as `@agent:planner` and then render as
// only `@agent`, with `:planner` floating loose. Excluding them keeps
// suggestion, render, and editor in lock-step.
const HANDLE_RE = /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/;

function addHandle(set: Set<string>, raw: string | undefined): void {
  if (!raw) return;
  const trimmed = raw.replace(/^@/, "").trim();
  if (!trimmed) return;
  if (!HANDLE_RE.test(trimmed)) return;
  set.add(trimmed);
}
