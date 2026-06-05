import type { Label } from "../../api/types.js";

export function nextCatalogLabelNames(assignedLabels: Label[], catalogLabels: Label[], toggledName: string): string[] {
  const catalogNames = new Set(catalogLabels.map((label) => label.name));
  const currentNames = assignedLabels.map((label) => label.name).filter((name) => catalogNames.has(name));

  return currentNames.includes(toggledName)
    ? currentNames.filter((name) => name !== toggledName)
    : [...currentNames, toggledName];
}
