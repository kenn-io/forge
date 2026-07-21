import { CompletionContext, type CompletionResult } from "@codemirror/autocomplete";
import { EditorState } from "@codemirror/state";
import { describe, expect, test, vi } from "vite-plus/test";

import type { KataTaskReference, KataTaskReferenceResponse, KataTaskReferenceSearch } from "../../api/kata/snapshot.js";
import { buildIssueCompletionSource } from "./issueCompletion";

function reference(overrides: Partial<KataTaskReference> = {}): KataTaskReference {
  return {
    uid: overrides.uid ?? "issue-rent",
    project_id: overrides.project_id ?? 1,
    project_uid: overrides.project_uid ?? "project-household",
    project_name: overrides.project_name ?? "household",
    short_id: overrides.short_id ?? "rent",
    qualified_id: overrides.qualified_id ?? "household#rent",
    reference: overrides.reference ?? "rent",
    title: overrides.title ?? "Pay rent",
  };
}

function response(references: KataTaskReference[]): KataTaskReferenceResponse {
  return {
    server_instance_id: "server-a",
    daemon_id: "home",
    generation: 7,
    invalidation_epoch: 2,
    fetched_at: "2026-07-20T12:00:00Z",
    references,
  };
}

function makeContext(text: string, explicit = false): CompletionContext {
  return new CompletionContext(EditorState.create({ doc: text }), text.length, explicit);
}

function makeSource(
  references: KataTaskReference[],
  options: { daemon?: () => string | undefined; debounceMs?: number } = {},
) {
  const searchReferences: KataTaskReferenceSearch = vi.fn(async () => response(references));
  const source = buildIssueCompletionSource({
    searchReferences,
    daemonId: options.daemon,
    debounceMs: options.debounceMs ?? 0,
  });
  return { source, searchReferences };
}

async function complete(
  source: ReturnType<typeof buildIssueCompletionSource>,
  text: string,
): Promise<CompletionResult | null> {
  return await source(makeContext(text));
}

describe("buildIssueCompletionSource", () => {
  test("uses the server reference for unique bare completions", async () => {
    const { source, searchReferences } = makeSource([reference()]);

    const result = await complete(source, "draft #re");

    expect(result?.options.map((option) => option.label)).toEqual(["#rent"]);
    expect(result?.options[0]?.apply).toBe("#rent");
    expect(searchReferences).toHaveBeenCalledWith("re", { limit: 50 });
  });

  test("uses the server-provided qualified reference for ambiguous short ids", async () => {
    const { source } = makeSource([
      reference({
        project_name: "Household display name",
        qualified_id: "household-identity#rent",
        reference: "household-identity#rent",
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
        project_id: 2,
        project_uid: "project-personal",
        project_name: "personal",
        short_id: "yoga",
        qualified_id: "personal#yoga",
        reference: "yoga",
        title: "Morning yoga",
      }),
    ]);

    const result = await complete(source, "done in household/#");

    expect(result?.options.map((option) => option.label)).toEqual(["household/#rent"]);
    expect(searchReferences).toHaveBeenCalledWith("household#", { limit: 50 });
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
    expect(searchReferences).toHaveBeenCalledWith("household-identity#r", { limit: 50 });
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

    expect(searchReferences).toHaveBeenCalledWith("ne", { daemon_id: "alpha", limit: 50 });
  });

  test("keeps the completion surface available when reference search fails", async () => {
    const searchReferences: KataTaskReferenceSearch = vi.fn(async () => {
      throw new Error("offline");
    });
    const source = buildIssueCompletionSource({ searchReferences });

    const result = await complete(source, "#rent");

    expect(result?.options).toEqual([]);
  });
});
