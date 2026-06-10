import type { Folder } from "../../api/docs/types";
import type { IssueCompletionOptions } from "./issueCompletion";
import type { IssueSummary, KataAPI } from "./docsIssueTypes";
import { effectiveDocsFolderDaemon } from "./folderDaemon";

export interface DocsIssueCompletionRuntime {
  folders: () => readonly Folder[];
  folderId: () => string | null | undefined;
  daemonRoster: () => readonly string[];
  activeDaemon: () => string | undefined;
  kataIssues: () => readonly IssueSummary[];
  kataSearch?: KataAPI["search"] | undefined;
}

export function buildDocsIssueCompletionOptions(runtime: DocsIssueCompletionRuntime): IssueCompletionOptions {
  const folderDaemon = () => effectiveDocsFolderDaemon(runtime.folders(), runtime.folderId(), runtime.daemonRoster());

  return {
    getIssues: () => (runtime.daemonRoster().length > 1 && folderDaemon() ? [] : runtime.kataIssues()),
    search: async (filters, daemonKey) => {
      if (!runtime.kataSearch) throw new Error("kata api not yet wired");
      return daemonKey ? runtime.kataSearch(filters, { daemonId: daemonKey }) : runtime.kataSearch(filters);
    },
    cacheKeyPrefix: () => folderDaemon() ?? runtime.activeDaemon() ?? "",
  };
}
