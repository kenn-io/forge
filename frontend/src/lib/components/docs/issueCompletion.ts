import type { Completion, CompletionContext, CompletionResult } from "@codemirror/autocomplete";
import { Effect, Exit } from "effect";

import type { KataTaskReference, KataTaskReferenceSearch } from "../../api/kata/snapshot.js";
import type { AppExecution, AppRuntime } from "../../app/runtime.js";

export interface IssueCompletionOptions {
  searchReferences: KataTaskReferenceSearch;
  daemonId?: (() => string | undefined) | undefined;
  debounceMs?: number | undefined;
}

interface IssueCompletionMatch {
  readonly from: number;
  readonly query: string;
  readonly qualifiedProject: string | null;
}

/**
 * Builds a CodeMirror completion source for open Kata task references.
 * Ambiguity comes from the server-provided `reference`; the client never
 * counts short ids or consults closed/all-status task state.
 */
export function buildIssueCompletionSource(options: IssueCompletionOptions, runtime: AppRuntime) {
  const debounceMs = options.debounceMs ?? 0;

  return function source(context: CompletionContext): Promise<CompletionResult | null> | null {
    const match = completionMatch(context);
    if (match === null || context.aborted) return null;
    const daemonId = options.daemonId?.();
    const execution = runtime.runCommand(
      Effect.gen(function* () {
        if (debounceMs > 0) yield* Effect.sleep(debounceMs);
        if (context.aborted) return null;
        const references = yield* options
          .searchReferences(match.query, {
            ...(daemonId ? { daemon_id: daemonId } : {}),
            limit: 50,
          })
          .pipe(
            Effect.map((response) => response.references ?? []),
            Effect.catch(() => Effect.succeed([])),
          );
        if (context.aborted) return null;
        const explicitlyQualified = match.qualifiedProject !== null;
        return {
          from: match.from,
          options: references
            .filter(
              (reference) =>
                match.qualifiedProject === null ||
                qualifiedProjectIdentity(reference.qualified_id) === match.qualifiedProject.toLocaleLowerCase(),
            )
            .map((reference) => completion(reference, explicitlyQualified)),
          filter: false,
        } satisfies CompletionResult;
      }),
      {
        operation: "complete Docs task reference",
        safeContext: { qualified: match.qualifiedProject !== null },
        onFailure: () => {},
      },
    );
    context.addEventListener("abort", execution.interrupt, { onDocChange: true });
    if (context.aborted) execution.interrupt();
    return observeCompletion(execution);
  };
}

function completionMatch(context: CompletionContext): IssueCompletionMatch | null {
  const qualified = context.matchBefore(/[A-Za-z][\w:.-]*\/#[a-z0-9]*/);
  if (qualified) {
    const separator = qualified.text.indexOf("/#");
    const project = qualified.text.slice(0, separator);
    const prefix = qualified.text.slice(separator + 2);
    return {
      from: qualified.from,
      query: `${project}#${prefix}`,
      qualifiedProject: project,
    };
  }

  const bare = context.matchBefore(/#[a-z0-9]*/);
  if (!bare) return null;
  if (bare.from > 0) {
    const before = context.state.doc.sliceString(bare.from - 1, bare.from);
    if (/\w/.test(before)) return null;
  }
  return {
    from: bare.from,
    query: bare.text.slice(1),
    qualifiedProject: null,
  };
}

async function observeCompletion(
  execution: AppExecution<CompletionResult | null, never>,
): Promise<CompletionResult | null> {
  const exit = await execution.exit;
  return Exit.isSuccess(exit) ? exit.value : null;
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
