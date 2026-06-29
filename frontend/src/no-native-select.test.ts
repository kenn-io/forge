import { readdirSync, readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vite-plus/test";

// Guards the design-system rule documented in context/ui-design-system.md
// (SelectDropdown section): visible app UI must not use native `<select>`
// controls; use the shared `SelectDropdown` primitive instead. This scans the
// component source trees so a reintroduced native select fails CI rather than
// only being caught in review.

const currentDir = path.dirname(fileURLToPath(import.meta.url));
const roots = [currentDir, path.resolve(currentDir, "../../packages/ui/src")];

// Matches the opening tag of a native `<select>` element. Case-sensitive so
// the `<SelectDropdown>` component and words like "selected"/"selection" are
// not flagged; the lookahead requires a tag boundary (whitespace, `>`, or `/`)
// so it only matches the element, not a substring.
const NATIVE_SELECT = /<select(?=[\s/>])/g;

function svelteFiles(dir: string): string[] {
  const out: string[] = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    if (entry.name === "node_modules" || entry.name === "dist") continue;
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) {
      out.push(...svelteFiles(full));
    } else if (entry.name.endsWith(".svelte")) {
      out.push(full);
    }
  }
  return out;
}

function violationsIn(file: string): string[] {
  const source = readFileSync(file, "utf8");
  const hits: string[] = [];
  for (const match of source.matchAll(NATIVE_SELECT)) {
    const line = source.slice(0, match.index).split("\n").length;
    hits.push(`${file}:${line}`);
  }
  return hits;
}

describe("no native select elements", () => {
  it("uses SelectDropdown instead of native <select> in app UI", () => {
    const files = roots.flatMap((root) => svelteFiles(root));
    // Sanity: ensure the scan actually reached the component trees.
    expect(files.length).toBeGreaterThan(0);

    const violations = files.flatMap(violationsIn);
    expect(
      violations,
      `Found native <select> elements. Use the SelectDropdown primitive from @middleman/ui instead:\n${violations.join("\n")}`,
    ).toEqual([]);
  });
});
