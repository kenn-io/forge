import { describe, expect, it } from "vitest";
import { parseToolTitle } from "./chat-tool-title.js";

describe("parseToolTitle", () => {
  it("treats execute calls and Claude grep command lines as shell", () => {
    expect(parseToolTitle("execute", "git status --short")).toEqual({ shell: true });
    expect(parseToolTitle("search", 'grep -n "handleRequest" src')).toEqual({ shell: true });
  });

  it("puts a read path in code and keeps its line range apart", () => {
    expect(parseToolTitle("read", "Read src/app.ts (1 - 50)")).toEqual({
      shell: false,
      parts: [
        { text: "Read ", role: "plain" },
        { text: "src/app.ts", role: "target" },
        { text: " (1 - 50)", role: "range" },
      ],
    });
    expect(parseToolTitle("read", "Read src/app.ts (from line 12)")).toMatchObject({
      parts: [{}, { text: "src/app.ts", role: "target" }, { text: " (from line 12)", role: "range" }],
    });
  });

  it("codes each backtick-quoted piece of a Claude Find title", () => {
    expect(parseToolTitle("search", "Find `src` `**/*.ts`")).toEqual({
      shell: false,
      parts: [
        { text: "Find ", role: "plain" },
        { text: "src", role: "target" },
        { text: " ", role: "plain" },
        { text: "**/*.ts", role: "target" },
      ],
    });
  });

  it('splits a Codex search into query and scope at the last " in "', () => {
    expect(parseToolTitle("search", "Search sign in flow in src/auth")).toEqual({
      shell: false,
      parts: [
        { text: "Search ", role: "plain" },
        { text: "sign in flow", role: "target" },
        { text: " in ", role: "plain" },
        { text: "src/auth", role: "target" },
      ],
    });
  });

  it("codes the whole target of edit and fetch titles", () => {
    expect(parseToolTitle("edit", "Edit src/server/routes.ts")).toEqual({
      shell: false,
      parts: [
        { text: "Edit ", role: "plain" },
        { text: "src/server/routes.ts", role: "target" },
      ],
    });
    expect(parseToolTitle("fetch", "Fetch https://example.com/docs")).toMatchObject({
      parts: [{ text: "Fetch " }, { text: "https://example.com/docs", role: "target" }],
    });
    expect(parseToolTitle("fetch", 'Search "release notes"')).toMatchObject({
      parts: [{ text: "Search " }, { text: '"release notes"', role: "target" }],
    });
  });

  it("leaves titles without a known verb and target as plain text", () => {
    expect(parseToolTitle("think", "Create task: tidy routes")).toEqual({
      shell: false,
      parts: [{ text: "Create task: tidy routes", role: "plain" }],
    });
    expect(parseToolTitle("search", "Search")).toEqual({ shell: false, parts: [{ text: "Search", role: "plain" }] });
    expect(parseToolTitle("read", "Reading the plan")).toEqual({
      shell: false,
      parts: [{ text: "Reading the plan", role: "plain" }],
    });
    expect(parseToolTitle(undefined, "Read file")).toEqual({
      shell: false,
      parts: [{ text: "Read file", role: "plain" }],
    });
    expect(parseToolTitle("other", "Edit settings")).toEqual({
      shell: false,
      parts: [{ text: "Edit settings", role: "plain" }],
    });
  });
});
