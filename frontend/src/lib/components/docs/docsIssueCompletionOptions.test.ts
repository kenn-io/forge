import { describe, expect, it, vi } from "vite-plus/test";
import { Effect } from "effect";

import type { Folder } from "../../api/docs/types";
import type { KataTaskReferenceSearch } from "../../api/kata/snapshot.js";
import { buildDocsIssueCompletionOptions } from "./docsIssueCompletionOptions";

const folders: Folder[] = [
  { id: "notes", name: "Notes", path: "/notes", daemon: "work" },
  { id: "archive", name: "Archive", path: "/archive", daemon: "gone" },
  { id: "inbox", name: "Inbox", path: "/inbox" },
];

const searchReferences: KataTaskReferenceSearch = vi.fn(() =>
  Effect.succeed({
    server_instance_id: "server-a",
    daemon_id: "home",
    generation: 7,
    invalidation_epoch: 2,
    fetched_at: "2026-07-20T12:00:00Z",
    references: [],
  }),
);

describe("buildDocsIssueCompletionOptions", () => {
  it("uses a live folder binding as the reference-search daemon", () => {
    const options = buildDocsIssueCompletionOptions({
      folders: () => folders,
      folderId: () => "notes",
      daemonRoster: () => ["home", "work"],
      activeDaemon: () => "home",
      searchReferences,
    });

    expect(options.daemonId?.()).toBe("work");
    expect(options.searchReferences).toBe(searchReferences);
  });

  it("uses the active daemon when a folder binding is stale", () => {
    const options = buildDocsIssueCompletionOptions({
      folders: () => folders,
      folderId: () => "archive",
      daemonRoster: () => ["home", "work"],
      activeDaemon: () => "home",
      searchReferences,
    });

    expect(options.daemonId?.()).toBe("home");
  });

  it("uses the bound daemon in single-daemon mode", () => {
    const options = buildDocsIssueCompletionOptions({
      folders: () => folders,
      folderId: () => "notes",
      daemonRoster: () => ["work"],
      activeDaemon: () => "work",
      searchReferences,
    });

    expect(options.daemonId?.()).toBe("work");
  });
});
