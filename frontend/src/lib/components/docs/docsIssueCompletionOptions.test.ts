import { describe, expect, it, vi } from "vite-plus/test";

import type { Folder } from "../../api/docs/types";
import type { KataReferenceSearch } from "../../api/kata/integration.js";
import { buildDocsIssueCompletionOptions } from "./docsIssueCompletionOptions";

const folders: Folder[] = [
  { id: "notes", name: "Notes", path: "/notes", daemon: "work" },
  { id: "archive", name: "Archive", path: "/archive", daemon: "gone" },
  { id: "inbox", name: "Inbox", path: "/inbox" },
];

const searchReferences: KataReferenceSearch = vi.fn(async () => []);

describe("buildDocsIssueCompletionOptions", () => {
  it("uses a live folder binding as the reference-search daemon", () => {
    const options = buildDocsIssueCompletionOptions({
      folders: () => folders,
      folderId: () => "notes",
      daemonRoster: () => ["home", "work"],
      searchReferences,
    });

    expect(options.daemonId?.()).toBe("work");
    expect(options.searchReferences).toBe(searchReferences);
  });

  it("does not fall back when a folder binding is stale", () => {
    const options = buildDocsIssueCompletionOptions({
      folders: () => folders,
      folderId: () => "archive",
      daemonRoster: () => ["home", "work"],
      searchReferences,
    });

    expect(options.daemonId?.()).toBeUndefined();
  });

  it("uses the bound daemon in single-daemon mode", () => {
    const options = buildDocsIssueCompletionOptions({
      folders: () => folders,
      folderId: () => "notes",
      daemonRoster: () => ["work"],
      searchReferences,
    });

    expect(options.daemonId?.()).toBe("work");
  });
});
