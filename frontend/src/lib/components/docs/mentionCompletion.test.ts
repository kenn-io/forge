import { describe, expect, test } from "vite-plus/test";
import { EditorState } from "@codemirror/state";
import { CompletionContext } from "@codemirror/autocomplete";
import { buildMentionCompletionSource, collectMentionNames } from "./mentionCompletion";
import type { IssueSummary } from "./docsIssueTypes";

function issue(partial: Partial<IssueSummary>): IssueSummary {
  return {
    id: 1,
    uid: "issue-1",
    project_id: 1,
    short_id: "rent",
    qualified_id: "household#rent",
    title: "Pay rent",
    status: "open",
    project_uid: "proj-household",
    project_name: "household",
    metadata: {},
    revision: 1,
    author: "wes",
    created_at: "2026-05-01T00:00:00Z",
    updated_at: "2026-05-01T00:00:00Z",
    ...partial,
  } as IssueSummary;
}

function makeContext(text: string): CompletionContext {
  const state = EditorState.create({ doc: text });
  return new CompletionContext(state, text.length, false);
}

describe("collectMentionNames", () => {
  test("returns distinct authors and owners as plain handles", () => {
    const names = collectMentionNames([
      issue({ author: "wes", owner: "claude" }),
      issue({ author: "wes" }),
      issue({ author: "anne" }),
    ]);
    expect(names).toEqual(["anne", "claude", "wes"]);
  });

  test("strips a leading @ if the source already includes one", () => {
    const names = collectMentionNames([issue({ author: "@bot" })]);
    expect(names).toEqual(["bot"]);
  });

  test("drops handles that contain whitespace", () => {
    const names = collectMentionNames([issue({ author: "Wes McKinney" }), issue({ author: "wes" })]);
    expect(names).toEqual(["wes"]);
  });
});

describe("buildMentionCompletionSource", () => {
  const source = buildMentionCompletionSource(() => ["wes", "claude", "anne", "bot"]);

  test("suggests names matching the typed prefix", () => {
    const result = source(makeContext("paged @cl"));
    expect(result).not.toBeNull();
    const labels = result!.options.map((o) => o.label);
    expect(labels).toContain("@claude");
    expect(labels).not.toContain("@wes");
  });

  test("includes substring matches after prefix matches", () => {
    const source2 = buildMentionCompletionSource(() => ["anne", "joanna", "annette"]);
    const result = source2(makeContext("@ann"));
    const labels = result!.options.map((o) => o.label);
    expect(labels[0]).toBe("@anne"); // prefix
    expect(labels[1]).toBe("@annette"); // prefix
    expect(labels[2]).toBe("@joanna"); // substring fallback
  });

  test("does not trigger when @ is preceded by a word char", () => {
    expect(source(makeContext("email@wes"))).toBeNull();
  });

  test("returns null with an empty name list", () => {
    const empty = buildMentionCompletionSource(() => []);
    expect(empty(makeContext("@an"))).toBeNull();
  });

  test("offers all names when only the @ trigger is present", () => {
    const result = source(makeContext("@"));
    expect(result).not.toBeNull();
    expect(result!.options.map((o) => o.label)).toEqual(expect.arrayContaining(["@anne", "@bot", "@claude", "@wes"]));
  });
});
