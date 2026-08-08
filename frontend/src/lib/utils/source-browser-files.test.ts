import { describe, expect, it } from "vite-plus/test";
import {
  buildSourceBrowserFileEntries,
  countSourceBrowserFileEntriesByCategory,
  filterSourceBrowserFileEntriesByCategory,
} from "./source-browser-files.js";

describe("source browser file entries", () => {
  it("carries last-changed metadata while preserving diff category filtering", () => {
    const entries = buildSourceBrowserFileEntries(
      [
        { path: "README.md", type: "blob", size: 12 },
        { path: "src/app.ts", type: "blob", size: 30 },
        { path: "src/app.test.ts", type: "blob", size: 40 },
        { path: "bun.lock", type: "blob", size: 50 },
      ],
      {
        "README.md": {
          sha: "abc",
          subject: "update docs",
          body: "",
          author_name: "Alice",
          author_email: "alice@example.com",
          authored_at: "2026-06-01T00:00:00Z",
        },
      },
    );

    expect(entries.map((entry) => [entry.path, entry.category])).toEqual([
      ["README.md", "plansDocs"],
      ["src/app.ts", "code"],
      ["src/app.test.ts", "tests"],
      ["bun.lock", "generated"],
    ]);
    expect(entries[0]?.lastChanged?.subject).toBe("update docs");
    expect(filterSourceBrowserFileEntriesByCategory(entries, "tests").map((entry) => entry.path)).toEqual([
      "src/app.test.ts",
    ]);
    expect(countSourceBrowserFileEntriesByCategory(entries)).toEqual({
      all: 4,
      plansDocs: 1,
      generated: 1,
      code: 1,
      tests: 1,
      other: 0,
    });
  });
});
