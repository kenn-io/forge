import { describe, expect, it, vi } from "vite-plus/test";

import type { Folder } from "../../api/docs/types";
import type { IssueSummary, SearchFilters } from "./docsIssueTypes";
import { buildDocsIssueCompletionOptions } from "./docsIssueCompletionOptions";

function issue(shortId: string): IssueSummary {
  return {
    uid: `issue-${shortId}`,
    short_id: shortId,
    qualified_id: `tasks#${shortId}`,
    title: `Task ${shortId}`,
    status: "open",
    project_name: "tasks",
  };
}

const folders: Folder[] = [
  { id: "notes", name: "Notes", path: "/notes", daemon: "work" },
  { id: "archive", name: "Archive", path: "/archive", daemon: "gone" },
  { id: "inbox", name: "Inbox", path: "/inbox" },
];

const filters: SearchFilters = {
  scope: { kind: "all" },
  status: "all",
  owner: "",
  label: "",
  query: "ren",
};

describe("buildDocsIssueCompletionOptions", () => {
  it("routes a live bound folder through its daemon and hides active-daemon local issues in multi-daemon mode", async () => {
    const local = [issue("rent")];
    const search = vi.fn(async () => ({ issues: [issue("renew")] }));
    const options = buildDocsIssueCompletionOptions({
      folders: () => folders,
      folderId: () => "notes",
      daemonRoster: () => ["home", "work"],
      activeDaemon: () => "home",
      kataIssues: () => local,
      kataSearch: search,
    });

    expect(options.getIssues()).toEqual([]);
    expect(options.cacheKeyPrefix?.()).toBe("work");

    await options.search?.(filters, options.cacheKeyPrefix?.() ?? "");

    expect(search).toHaveBeenCalledWith(filters, { daemonId: "work" });
  });

  it("falls back to the active daemon when a folder binding is stale", async () => {
    const local = [issue("rent")];
    const search = vi.fn(async () => ({ issues: [issue("renew")] }));
    const options = buildDocsIssueCompletionOptions({
      folders: () => folders,
      folderId: () => "archive",
      daemonRoster: () => ["home", "work"],
      activeDaemon: () => "home",
      kataIssues: () => local,
      kataSearch: search,
    });

    expect(options.getIssues()).toBe(local);
    expect(options.cacheKeyPrefix?.()).toBe("home");

    await options.search?.(filters, options.cacheKeyPrefix?.() ?? "");

    expect(search).toHaveBeenCalledWith(filters, { daemonId: "home" });
  });

  it("keeps local issues for single-daemon mode even when the folder has a binding", () => {
    const local = [issue("rent")];
    const options = buildDocsIssueCompletionOptions({
      folders: () => folders,
      folderId: () => "notes",
      daemonRoster: () => ["work"],
      activeDaemon: () => "work",
      kataIssues: () => local,
      kataSearch: async () => ({ issues: [] }),
    });

    expect(options.getIssues()).toBe(local);
    expect(options.cacheKeyPrefix?.()).toBe("work");
  });
});
