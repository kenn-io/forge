import { readFileSync, readdirSync, statSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vite-plus/test";

// `@middleman/ui` and `@kenn-io/kit-ui` are excluded from Vite's optimizer, so every
// lucide icon they import is discovered lazily. A cold optimizer that meets an
// unlisted one mid-run re-bundles and reloads the page underneath whatever is
// mounted -- which in the dev server shows up as an icon that renders wrong, and in
// the browser test tier as a mount that vanishes. The config carries the full list
// to stop that, and this keeps the list honest: it drifted twice before it existed,
// once for an icon that had never been added and once for two left behind by a
// control that was deleted.
const here = path.dirname(fileURLToPath(import.meta.url));
const frontendDir = path.resolve(here, "..");
const ICON_IMPORT = /@lucide\/svelte\/icons\/[a-z0-9-]+/g;

function sourceFiles(dir: string): string[] {
  return readdirSync(dir).flatMap((entry) => {
    const full = path.join(dir, entry);
    if (statSync(full).isDirectory()) return entry === "node_modules" ? [] : sourceFiles(full);
    return /\.(svelte|ts|js)$/.test(entry) ? [full] : [];
  });
}

function importedIcons(): Set<string> {
  const roots = [path.join(frontendDir, "src"), path.resolve(frontendDir, "../packages/ui/src")];
  const found = new Set<string>();
  for (const root of roots) {
    for (const file of sourceFiles(root)) {
      // This file names icon paths in prose; counting them would make the guard
      // assert against itself.
      if (file === fileURLToPath(import.meta.url)) continue;
      for (const match of readFileSync(file, "utf8").matchAll(ICON_IMPORT)) found.add(match[0]);
    }
  }
  return found;
}

describe("vite optimizeDeps lucide coverage", () => {
  it("pre-bundles every lucide icon the frontend and packages/ui import", () => {
    const config = readFileSync(path.join(frontendDir, "vite.config.ts"), "utf8");
    // Bare entries only: Vite registers `"pkg > icon"` and `"icon"` under
    // different module IDs, so a nested kit-ui entry does not pre-bundle the
    // same icon imported directly from frontend or packages/ui source.
    const listed = new Set([...config.matchAll(/"(@lucide\/svelte\/icons\/[a-z0-9-]+)"/g)].map((match) => match[1]!));

    const missing = [...importedIcons()].filter((icon) => !listed.has(icon)).sort();
    expect(missing).toEqual([]);
  });
});
