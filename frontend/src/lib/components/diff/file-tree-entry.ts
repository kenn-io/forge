import type { FileTreeOptions } from "@pierre/trees";

type TreeGitStatus = NonNullable<FileTreeOptions["gitStatus"]>[number];

export interface FileTreeEntry {
  path: string;
  status?: TreeGitStatus["status"] | undefined;
  decoration?: string | null | undefined;
  decorationTitle?: string | undefined;
}
