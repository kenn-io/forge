import { describe, expect, it } from "vite-plus/test";

import { groupResults, parsePaletteQuery } from "./palette-search.svelte.js";
import type { Action } from "./types.js";
import type { Issue, PullRequest } from "@middleman/ui/api/types";

const noop = (): void => {};
const trueWhen = (): boolean => true;

function action(id: string, label = id): Action {
  return {
    id,
    label,
    scope: "global",
    binding: null,
    priority: 0,
    when: trueWhen,
    handler: noop,
  };
}

function pull(num: number, title: string): PullRequest {
  return { Number: num, Title: title } as PullRequest;
}

function issue(num: number, title: string): Issue {
  return { Number: num, Title: title } as Issue;
}

describe("parsePaletteQuery", () => {
  it("recognizes the > command prefix and trims the remainder", () => {
    expect(parsePaletteQuery(">refresh")).toEqual({
      scope: "command",
      query: "refresh",
    });
    expect(parsePaletteQuery("> refresh ")).toEqual({
      scope: "command",
      query: "refresh",
    });
  });

  it("recognizes pr: prefix", () => {
    expect(parsePaletteQuery("pr:fix")).toEqual({
      scope: "pr",
      query: "fix",
    });
  });

  it("recognizes issue: prefix", () => {
    expect(parsePaletteQuery("issue:bug")).toEqual({
      scope: "issue",
      query: "bug",
    });
  });

  it("falls back to all-scope for plain input", () => {
    expect(parsePaletteQuery("plain")).toEqual({
      scope: "all",
      query: "plain",
    });
    expect(parsePaletteQuery("  spaced  ")).toEqual({
      scope: "all",
      query: "spaced",
    });
    expect(parsePaletteQuery("")).toEqual({ scope: "all", query: "" });
  });

  it("returns the reserved marker for repo: and ws: with empty query", () => {
    const repo = parsePaletteQuery("repo:abc");
    const ws = parsePaletteQuery("ws:abc");
    expect(repo).toEqual({ scope: "reserved", query: "" });
    expect(ws).toEqual({ scope: "reserved", query: "" });
    // Type-level guard: query is the literal "" string.
    if (repo.scope === "reserved") {
      const empty: "" = repo.query;
      expect(empty).toBe("");
    }
  });
});

describe("groupResults", () => {
  const commands = [action("cmd.alpha", "Alpha command"), action("cmd.beta", "Beta")];
  const pulls = [pull(1, "Fix bug"), pull(2, "Add feature")];
  const issues = [issue(10, "Crash on launch"), issue(11, "Add feature request")];

  it("scope=command suppresses pulls and issues", () => {
    const out = groupResults({
      commands,
      pulls,
      issues,
      parsed: { scope: "command", query: "alpha" },
    });
    expect(out.commands.map((c) => c.id)).toEqual(["cmd.alpha"]);
    expect(out.pulls).toEqual([]);
    expect(out.issues).toEqual([]);
  });

  it("scope=pr suppresses commands and issues", () => {
    const out = groupResults({
      commands,
      pulls,
      issues,
      parsed: { scope: "pr", query: "fix" },
    });
    expect(out.commands).toEqual([]);
    expect(out.pulls.map((p) => p.Number)).toEqual([1]);
    expect(out.issues).toEqual([]);
  });

  it("scope=issue suppresses commands and pulls", () => {
    const out = groupResults({
      commands,
      pulls,
      issues,
      parsed: { scope: "issue", query: "crash" },
    });
    expect(out.commands).toEqual([]);
    expect(out.pulls).toEqual([]);
    expect(out.issues.map((i) => i.Number)).toEqual([10]);
  });

  it("scope=all with empty query returns all input items unfiltered up to the cap", () => {
    const out = groupResults({
      commands,
      pulls,
      issues,
      parsed: { scope: "all", query: "" },
    });
    expect(out.commands).toHaveLength(commands.length);
    expect(out.pulls).toHaveLength(pulls.length);
    expect(out.issues).toHaveLength(issues.length);
  });

  it("scope=all with text filters by substring across all three groups", () => {
    const out = groupResults({
      commands,
      pulls,
      issues,
      parsed: { scope: "all", query: "feature" },
    });
    expect(out.commands).toEqual([]);
    expect(out.pulls.map((p) => p.Number)).toEqual([2]);
    expect(out.issues.map((i) => i.Number)).toEqual([11]);
  });

  it("scope=reserved returns three empty arrays regardless of inputs", () => {
    const out = groupResults({
      commands,
      pulls,
      issues,
      parsed: { scope: "reserved", query: "" },
    });
    expect(out).toEqual({ commands: [], pulls: [], issues: [] });
  });

  it("caps each group at 10 entries after filtering", () => {
    const manyCommands = Array.from({ length: 15 }, (_, i) => action(`cmd.${i}`, `match-${i}`));
    const manyPulls = Array.from({ length: 15 }, (_, i) => pull(i, `match-${i}`));
    const manyIssues = Array.from({ length: 15 }, (_, i) => issue(i, `match-${i}`));
    const out = groupResults({
      commands: manyCommands,
      pulls: manyPulls,
      issues: manyIssues,
      parsed: { scope: "all", query: "match" },
    });
    expect(out.commands).toHaveLength(10);
    expect(out.pulls).toHaveLength(10);
    expect(out.issues).toHaveLength(10);
  });

  it("caps each group at 10 entries even when query is empty (unfiltered slice)", () => {
    const many = Array.from({ length: 15 }, (_, i) => pull(i, `t-${i}`));
    const out = groupResults({
      commands: [],
      pulls: many,
      issues: [],
      parsed: { scope: "pr", query: "" },
    });
    expect(out.pulls).toHaveLength(10);
  });
});
