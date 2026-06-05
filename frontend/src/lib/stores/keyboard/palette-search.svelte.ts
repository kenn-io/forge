import type { Action } from "./types.js";
import type { Issue, PullRequest } from "@middleman/ui/api/types";

export type ParsedQuery =
  | { scope: "command"; query: string }
  | { scope: "pr"; query: string }
  | { scope: "issue"; query: string }
  | { scope: "all"; query: string }
  | { scope: "reserved"; query: "" };

export interface GroupResultsInput {
  commands: Action[];
  pulls: PullRequest[];
  issues: Issue[];
  parsed: ParsedQuery;
}

export interface GroupedResults {
  commands: Action[];
  pulls: PullRequest[];
  issues: Issue[];
}

const GROUP_CAP = 10;

export function parsePaletteQuery(input: string): ParsedQuery {
  if (input.startsWith(">")) {
    return { scope: "command", query: input.slice(1).trim() };
  }
  if (input.startsWith("pr:")) {
    return { scope: "pr", query: input.slice(3).trim() };
  }
  if (input.startsWith("issue:")) {
    return { scope: "issue", query: input.slice(6).trim() };
  }
  if (input.startsWith("repo:") || input.startsWith("ws:")) {
    return { scope: "reserved", query: "" };
  }
  return { scope: "all", query: input.trim() };
}

function matchAction(action: Action, needle: string): boolean {
  return action.id.toLowerCase().includes(needle) || action.label.toLowerCase().includes(needle);
}

function matchPull(pull: PullRequest, needle: string): boolean {
  return pull.Title.toLowerCase().includes(needle);
}

function matchIssue(issue: Issue, needle: string): boolean {
  return issue.Title.toLowerCase().includes(needle);
}

function filterCap<T>(items: T[], predicate: (item: T) => boolean): T[] {
  const out: T[] = [];
  for (const item of items) {
    if (!predicate(item)) continue;
    out.push(item);
    if (out.length >= GROUP_CAP) break;
  }
  return out;
}

export function groupResults(input: GroupResultsInput): GroupedResults {
  const { commands, pulls, issues, parsed } = input;
  if (parsed.scope === "reserved") {
    return { commands: [], pulls: [], issues: [] };
  }

  const needle = parsed.query.toLowerCase();
  const allMatch = needle.length === 0;

  const filteredCommands = allMatch ? commands.slice(0, GROUP_CAP) : filterCap(commands, (a) => matchAction(a, needle));
  const filteredPulls = allMatch ? pulls.slice(0, GROUP_CAP) : filterCap(pulls, (p) => matchPull(p, needle));
  const filteredIssues = allMatch ? issues.slice(0, GROUP_CAP) : filterCap(issues, (i) => matchIssue(i, needle));

  switch (parsed.scope) {
    case "command":
      return { commands: filteredCommands, pulls: [], issues: [] };
    case "pr":
      return { commands: [], pulls: filteredPulls, issues: [] };
    case "issue":
      return { commands: [], pulls: [], issues: filteredIssues };
    case "all":
      return {
        commands: filteredCommands,
        pulls: filteredPulls,
        issues: filteredIssues,
      };
  }
}
