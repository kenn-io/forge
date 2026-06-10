import type { Folder } from "../../api/docs/types";

export function effectiveDocsFolderDaemon(
  folders: readonly Folder[],
  folderId: string | null | undefined,
  roster: readonly string[],
): string | undefined {
  if (!folderId) return undefined;
  const bound = folders.find((folder) => folder.id === folderId)?.daemon;
  if (!bound) return undefined;
  return roster.includes(bound) ? bound : undefined;
}
