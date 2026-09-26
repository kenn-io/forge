import { describe, expect, it } from "vite-plus/test";
import { matchesSearchQuery, parseSearchQuery } from "./search-query.js";

describe("parseSearchQuery", () => {
  it.each([
    ["fix bug", { include: ["fix", "bug"], exclude: [] }],
    ["!alice", { include: [], exclude: ["alice"] }],
    ["NOT alice", { include: [], exclude: ["alice"] }],
    ["! alice", { include: [], exclude: ["alice"] }],
    ['fix NOT "needs review"', { include: ["fix"], exclude: ["needs review"] }],
    ['!"needs review"', { include: [], exclude: ["needs review"] }],
    ["do not merge", { include: ["do", "not", "merge"], exclude: [] }],
    ['"NOT" "!"', { include: ["not", "!"], exclude: [] }],
    ["wow!", { include: ["wow!"], exclude: [] }],
    ["fix NOT", { include: ["fix"], exclude: [] }],
    ["fix !", { include: ["fix"], exclude: [] }],
    ["can't", { include: ["can't"], exclude: [] }],
  ])("parses %s", (search, want) => {
    expect(parseSearchQuery(search)).toEqual(want);
  });
});

describe("matchesSearchQuery", () => {
  const fields = ["Fix login flow", "alice", "acme/widgets"];

  it("requires every included term in some field and rejects excluded terms", () => {
    expect(matchesSearchQuery(parseSearchQuery("fix acme"), fields)).toBe(true);
    expect(matchesSearchQuery(parseSearchQuery("fix NOT ALICE"), fields)).toBe(false);
    expect(matchesSearchQuery(parseSearchQuery("!bob"), fields)).toBe(true);
    expect(matchesSearchQuery(parseSearchQuery("fix missing"), fields)).toBe(false);
  });
});
