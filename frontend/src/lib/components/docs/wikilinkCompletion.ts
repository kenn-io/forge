import type { Completion, CompletionContext, CompletionResult } from "@codemirror/autocomplete";
import type { EditorView } from "@codemirror/view";
import type { FolderIndex } from "../../api/docs/folderLinks";

/**
 * Builds a CodeMirror completion source that triggers on `[[` and
 * suggests docs from the current folder. Basenames are inserted bare
 * when unique; otherwise the full rel_path is inserted so the wikilink
 * resolver can't pick the wrong file.
 *
 * The closing `]]` is inserted as part of the apply step and the caret
 * lands after it, so the user can keep typing without thinking about
 * the brackets the bracket-matching extension already opened.
 */
export function buildWikilinkCompletionSource(getIndex: () => FolderIndex) {
  return function source(context: CompletionContext): CompletionResult | null {
    // Match `[[` followed by any non-closing-bracket / non-newline run.
    // The body intentionally allows characters that wikilink targets
    // accept — letters, digits, spaces, slashes, hyphens — so a user
    // typing `[[my note` keeps the menu open.
    const match = context.matchBefore(/\[\[[^\]\n]*/);
    if (!match) return null;
    const query = match.text.slice(2);
    const index = getIndex();
    if (index.byPath.size === 0) return null;
    const lower = query.toLowerCase();

    const seen = new Set<string>();
    const options: Completion[] = [];
    for (const paths of index.byBasename.values()) {
      // Each path is the canonical rel_path; insert as basename when
      // unambiguous, full path otherwise. Use the path's original
      // casing for the visible label so suggestions match what the
      // user sees in the folder tree.
      const ambiguous = paths.length > 1;
      for (const path of paths) {
        if (seen.has(path)) continue;
        seen.add(path);
        const candidate = wikilinkCandidate(path, ambiguous);
        if (!matchesQuery(candidate, lower)) continue;
        options.push(buildOption(candidate, path));
      }
    }
    options.sort(rankCompletion(lower));

    return {
      from: match.from,
      options: options.slice(0, 50),
      filter: false,
      validFor: /^\[\[[^\]\n]*$/,
    };
  };
}

interface WikilinkCandidate {
  // Text that goes inside the brackets — basename when unique, rel_path
  // (no extension) when shared.
  target: string;
  // basename (no extension) used for ranking and display.
  basename: string;
  // Lowercased forms cached so the ranker doesn't re-lower per compare.
  lowerTarget: string;
  lowerBasename: string;
  lowerPath: string;
}

function wikilinkCandidate(path: string, ambiguous: boolean): WikilinkCandidate {
  const basename = basenameNoExt(path);
  // For an unambiguous file, insert the bare basename.
  // For an ambiguous nested file (path contains "/"), insert the full
  // rel_path without extension so the path lookup resolves uniquely.
  // For an ambiguous root-level file, stripExtension(path) collapses
  // back to the bare basename — keep the extension instead so the
  // resolver routes through the explicit path lookup rather than the
  // basename map, which would otherwise stay ambiguous.
  let target: string;
  if (!ambiguous) {
    target = basename;
  } else if (path.includes("/")) {
    target = stripExtension(path);
  } else {
    target = path;
  }
  return {
    target,
    basename,
    lowerTarget: target.toLowerCase(),
    lowerBasename: basename.toLowerCase(),
    lowerPath: stripExtension(path).toLowerCase(),
  };
}

function basenameNoExt(path: string): string {
  const last = path.includes("/") ? path.slice(path.lastIndexOf("/") + 1) : path;
  return stripExtension(last);
}

function buildOption(candidate: WikilinkCandidate, path: string): Completion {
  const insert = `[[${candidate.target}]]`;
  const option: Completion = {
    label: candidate.target,
    type: "file",
    // Use a function so the caret lands after `]]` instead of inside
    // the brackets where matchBefore would re-trigger the menu. Also
    // consume the trailing `]]` that closeBrackets auto-inserts when
    // the user typed the opening `[[` — otherwise the apply would
    // leave stray brackets after the wikilink.
    apply: (view: EditorView, _completion: Completion, from: number, to: number) => {
      const tail = view.state.doc.sliceString(to, to + 2);
      const adjustedTo = tail === "]]" ? to + 2 : to;
      view.dispatch({
        changes: { from, to: adjustedTo, insert },
        selection: { anchor: from + insert.length },
      });
    },
  };
  if (candidate.target === candidate.basename) {
    option.detail = path;
  }
  return option;
}

function matchesQuery(candidate: WikilinkCandidate, lower: string): boolean {
  if (!lower) return true;
  if (candidate.lowerTarget.includes(lower)) return true;
  if (candidate.lowerBasename.includes(lower)) return true;
  if (candidate.lowerPath.includes(lower)) return true;
  return false;
}

function rankCompletion(lower: string) {
  return (a: Completion, b: Completion): number => {
    if (!lower) {
      // Alpha sort by label when there's no query so the first menu
      // pop is predictable.
      return compareLabels(a.label, b.label);
    }
    const aScore = score(a.label.toLowerCase(), lower);
    const bScore = score(b.label.toLowerCase(), lower);
    if (aScore !== bScore) return aScore - bScore;
    return compareLabels(a.label, b.label);
  };
}

// Smaller is better: prefix match beats substring match.
function score(label: string, lower: string): number {
  if (label.startsWith(lower)) return 0;
  if (label.includes(lower)) return 1;
  return 2;
}

function compareLabels(a: string, b: string): number {
  return a.localeCompare(b);
}

function stripExtension(path: string): string {
  return path.replace(/\.(md|markdown)$/i, "");
}
