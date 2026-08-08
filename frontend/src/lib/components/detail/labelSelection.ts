import type { Label } from "../../api/types.js";

export function nextCatalogLabels(assignedLabels: Label[], catalogLabels: Label[], toggledName: string): Label[] {
  const catalogNames = new Set(catalogLabels.map((label) => label.name));
  const currentNames = assignedLabels.map((label) => label.name).filter((name) => catalogNames.has(name));

  const nextNames = currentNames.includes(toggledName)
    ? currentNames.filter((name) => name !== toggledName)
    : [...currentNames, toggledName];
  return nextNames.flatMap((name) => {
    const label = catalogLabels.find((candidate) => candidate.name === name);
    return label === undefined ? [] : [label];
  });
}
