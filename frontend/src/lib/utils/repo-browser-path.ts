import type { RepoBrowserTreeEntry } from "../api/types.js";

export function chooseRepoBrowserInitialPath(
  entries: readonly Pick<RepoBrowserTreeEntry, "path" | "type">[],
): string | null {
  const files = entries.filter((entry) => entry.type === "file" || entry.type === "blob");
  const rootReadme = files.find((entry) => isReadme(entry.path) && !entry.path.includes("/"));
  const nestedReadme = files.find((entry) => isReadme(entry.path));
  return rootReadme?.path ?? nestedReadme?.path ?? files[0]?.path ?? null;
}

function isReadme(path: string): boolean {
  const basename =
    path
      .split(/[\\/]+/)
      .filter(Boolean)
      .at(-1) ?? "";
  return /^readme(?:\.[^.]+)?$/i.test(basename);
}
