import type { Completion, CompletionContext, CompletionResult } from "@codemirror/autocomplete";

import type { KataTaskReference, KataTaskReferenceSearch } from "../../api/kata/snapshot.js";

export interface IssueCompletionOptions {
  searchReferences: KataTaskReferenceSearch;
  daemonId?: (() => string | undefined) | undefined;
  debounceMs?: number | undefined;
}

/**
 * Builds a CodeMirror completion source for open Kata task references.
 * Ambiguity comes from the server-provided `reference`; the client never
 * counts short ids or consults closed/all-status task state.
 */
export function buildIssueCompletionSource(options: IssueCompletionOptions) {
  const debounceMs = options.debounceMs ?? 0;

  return async function source(context: CompletionContext): Promise<CompletionResult | null> {
    const qualified = context.matchBefore(/[A-Za-z][\w:.-]*\/#[a-z0-9]*/);
    if (qualified) {
      const separator = qualified.text.indexOf("/#");
      const project = qualified.text.slice(0, separator);
      const prefix = qualified.text.slice(separator + 2);
      const references = await loadReferences(`${project}#${prefix}`, options, debounceMs, context);
      if (context.aborted) return null;
      return {
        from: qualified.from,
        options: references
          .filter((reference) => qualifiedProjectIdentity(reference.qualified_id) === project.toLocaleLowerCase())
          .map((reference) => completion(reference, true)),
        filter: false,
      };
    }

    const bare = context.matchBefore(/#[a-z0-9]*/);
    if (!bare) return null;
    if (bare.from > 0) {
      const before = context.state.doc.sliceString(bare.from - 1, bare.from);
      if (/\w/.test(before)) return null;
    }

    const references = await loadReferences(bare.text.slice(1), options, debounceMs, context);
    if (context.aborted) return null;
    return {
      from: bare.from,
      options: references.map((reference) => completion(reference, false)),
      filter: false,
    };
  };
}

async function loadReferences(
  query: string,
  options: IssueCompletionOptions,
  debounceMs: number,
  context: CompletionContext,
): Promise<readonly KataTaskReference[]> {
  const daemonId = options.daemonId?.();
  if (debounceMs > 0) {
    await new Promise<void>((resolve) => setTimeout(resolve, debounceMs));
    if (context.aborted) return [];
  }

  try {
    const response = await options.searchReferences(query, {
      ...(daemonId ? { daemon_id: daemonId } : {}),
      limit: 50,
    });
    return context.aborted ? [] : (response.references ?? []);
  } catch {
    return [];
  }
}

function completion(reference: KataTaskReference, explicitlyQualified: boolean): Completion {
  const serverQualified = reference.reference !== reference.short_id;
  const apply =
    explicitlyQualified || serverQualified
      ? markdownQualifiedReference(explicitlyQualified ? reference.qualified_id : reference.reference)
      : `#${reference.reference}`;
  return {
    label: apply,
    detail: explicitlyQualified ? reference.title : `${reference.title}  ·  ${reference.project_name}`,
    type: "variable",
    apply,
  };
}

function markdownQualifiedReference(reference: string): string {
  const delimiter = reference.lastIndexOf("#");
  return `${reference.slice(0, delimiter)}/#${reference.slice(delimiter + 1)}`;
}

function qualifiedProjectIdentity(qualifiedID: string): string {
  return qualifiedID.slice(0, qualifiedID.lastIndexOf("#")).toLocaleLowerCase();
}
