import type { Folder } from "../../api/docs/types";
import type { KataTaskReferenceSearch } from "../../api/kata/snapshot.js";
import type { IssueCompletionOptions } from "./issueCompletion";
import { effectiveDocsFolderDaemon } from "./folderDaemon";

export interface DocsIssueCompletionRuntime {
  folders: () => readonly Folder[];
  folderId: () => string | null | undefined;
  daemonRoster: () => readonly string[];
  activeDaemon: () => string | undefined;
  searchReferences: KataTaskReferenceSearch;
}

export function buildDocsIssueCompletionOptions(runtime: DocsIssueCompletionRuntime): IssueCompletionOptions {
  const folderDaemon = () => effectiveDocsFolderDaemon(runtime.folders(), runtime.folderId(), runtime.daemonRoster());

  return {
    searchReferences: runtime.searchReferences,
    daemonId: () => folderDaemon() ?? runtime.activeDaemon(),
  };
}
