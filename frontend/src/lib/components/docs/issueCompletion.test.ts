import { CompletionContext, type CompletionResult } from "@codemirror/autocomplete";
import { EditorState } from "@codemirror/state";
import { Effect } from "effect";
import { afterAll, describe, expect, test, vi } from "vite-plus/test";

import type { KataIssueReference, KataReferenceSearch } from "../../api/kata/integration.js";
import { makeAppRuntime } from "../../app/runtime.js";
import { buildIssueCompletionSource } from "./issueCompletion";

const runtime = makeAppRuntime();

afterAll(async () => {
  await Effect.runPromise(runtime.disposeEffect);
});

function reference(overrides: Partial<KataIssueReference> = {}): KataIssueReference {
  return {
    uid: overrides.uid ?? "issue-rent",
    project_uid: overrides.project_uid ?? "project-household",
    project_name: overrides.project_name ?? "household",
    short_id: overrides.short_id ?? "rent",
    qualified_id: overrides.qualified_id ?? "household#rent",
    title: overrides.title ?? "Pay rent",
    status: overrides.status ?? "open",
  };
}

function makeContext(text: string, explicit = false): CompletionContext {
  return new CompletionContext(EditorState.create({ doc: text }), text.length, explicit);
}

function makeSource(
  references: KataIssueReference[],
  options: { daemon?: () => string | undefined; debounceMs?: number } = { daemon: () => "home" },
) {
  const searchReferences: KataReferenceSearch = vi.fn(async () => references);
  const source = buildIssueCompletionSource(
    {
      searchReferences,
      daemonId: options.daemon ?? (() => "home"),
      debounceMs: options.debounceMs ?? 0,
    },
    runtime,
  );
  return { source, searchReferences };
}

async function complete(
  source: ReturnType<typeof buildIssueCompletionSource>,
  text: string,
): Promise<CompletionResult | null> {
  return await source(makeContext(text));
}

describe("buildIssueCompletionSource", () => {
  test("uses canonical qualified references for bare completions", async () => {
    const { source, searchReferences } = makeSource([reference()]);

    const result = await complete(source, "draft #re");

    expect(result?.options.map((option) => option.label)).toEqual(["household/#rent"]);
    expect(result?.options[0]?.apply).toBe("household/#rent");
    expect(searchReferences).toHaveBeenCalledWith("home", "re", expect.any(AbortSignal));
  });

  test("uses the server-provided qualified reference for ambiguous short ids", async () => {
    const { source } = makeSource([
      reference({
        project_name: "Household display name",
        qualified_id: "household-identity#rent",
      }),
    ]);

    const result = await complete(source, "draft #re");

    expect(result?.options.map((option) => option.label)).toEqual(["household-identity/#rent"]);
    expect(result?.options[0]?.apply).toBe("household-identity/#rent");
  });

  test("filters qualified completion results to the requested project", async () => {
    const { source, searchReferences } = makeSource([
      reference(),
      reference({
        uid: "issue-yoga",
        project_uid: "project-personal",
        project_name: "personal",
        short_id: "yoga",
        qualified_id: "personal#yoga",
        title: "Morning yoga",
      }),
    ]);

    const result = await complete(source, "done in household/#");

    expect(result?.options.map((option) => option.label)).toEqual(["household/#rent"]);
    expect(searchReferences).toHaveBeenCalledWith("home", "household#", expect.any(AbortSignal));
  });

  test("scopes qualified completion by canonical identity instead of project display name", async () => {
    const { source, searchReferences } = makeSource([
      reference({
        project_name: "Household display name",
        qualified_id: "household-identity#rent",
      }),
    ]);

    const result = await complete(source, "done in household-identity/#r");

    expect(result?.options.map((option) => option.label)).toEqual(["household-identity/#rent"]);
    expect(searchReferences).toHaveBeenCalledWith("home", "household-identity#r", expect.any(AbortSignal));
  });

  test("returns no completion for a formerly closed task omitted by the open reference service", async () => {
    const { source } = makeSource([]);

    const result = await complete(source, "see #rentprev");

    expect(result?.options).toEqual([]);
  });

  test("does not trigger inside a word", async () => {
    const { source, searchReferences } = makeSource([reference()]);

    expect(await complete(source, "issue#re")).toBeNull();
    expect(searchReferences).not.toHaveBeenCalled();
  });

  test("captures and forwards the daemon selected before debounce", async () => {
    let daemon = "alpha";
    const { source, searchReferences } = makeSource([], {
      daemon: () => daemon,
      debounceMs: 5,
    });

    const pending = complete(source, "#ne");
    daemon = "beta";
    await pending;

    expect(searchReferences).toHaveBeenCalledWith("alpha", "ne", expect.any(AbortSignal));
  });

  test("keeps the completion surface available when reference search fails", async () => {
    const searchReferences: KataReferenceSearch = vi.fn(async () => {
      throw new Error("offline");
    });
    const source = buildIssueCompletionSource({ searchReferences, daemonId: () => "home" }, runtime);

    const result = await complete(source, "#rent");

    expect(result?.options).toEqual([]);
  });

  test("interrupts reference search when CodeMirror aborts the completion", async () => {
    let searchInterrupted = false;
    let abortCompletion: (() => void) | undefined;
    const searchReferences: KataReferenceSearch = vi.fn(
      (_daemon, _query, signal) =>
        new Promise<never>((_resolve, reject) => {
          signal?.addEventListener("abort", () => {
            searchInterrupted = true;
            reject(new DOMException("Aborted", "AbortError"));
          });
        }),
    );
    const source = buildIssueCompletionSource({ searchReferences, daemonId: () => "home" }, runtime);
    const context = makeContext("#rent");
    vi.spyOn(context, "addEventListener").mockImplementation((_type, listener) => {
      abortCompletion = listener;
    });

    const pending = source(context);
    await vi.waitFor(() => expect(searchReferences).toHaveBeenCalled());
    abortCompletion?.();

    await expect(pending).resolves.toBeNull();
    expect(searchInterrupted).toBe(true);
  });
});
