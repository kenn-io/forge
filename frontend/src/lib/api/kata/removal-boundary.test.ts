import { existsSync, readFileSync } from "node:fs";
import { resolve } from "node:path";
import ts from "typescript";
import { describe, expect, it } from "vite-plus/test";

import { getRoute, navigate } from "../../stores/router.svelte.js";

const frontendRoot = resolve(import.meta.dirname, "../../../..");
const repositoryRoot = resolve(frontendRoot, "..");

const removedPaths = [
  "src/lib/features/kata",
  "src/lib/api/kata/eventStream.ts",
  "src/lib/api/kata/navigation.ts",
  "src/lib/api/kata/snapshot.ts",
  "src/lib/api/kata/taskClient.ts",
  "src/lib/api/kata/taskTypes.ts",
  "src/lib/stores/active-kata-daemon.svelte.ts",
  "src/lib/stores/kata-authority.svelte.ts",
];

function moduleImports(relativePath: string): string[] {
  const absolutePath = resolve(repositoryRoot, relativePath);
  const source = readFileSync(absolutePath, "utf8");
  const script = absolutePath.endsWith(".svelte")
    ? [...source.matchAll(/<script(?:\s[^>]*)?>([\s\S]*?)<\/script>/g)].map((match) => match[1]).join("\n")
    : source;
  const file = ts.createSourceFile(absolutePath, script, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS);
  return file.statements
    .filter(ts.isImportDeclaration)
    .map((statement) => statement.moduleSpecifier)
    .filter(ts.isStringLiteral)
    .map((specifier) => specifier.text);
}

describe("Kata integration removal boundary", () => {
  it("treats the removed full Kata route as unknown", () => {
    navigate("/kata?issue=issue-a");
    expect(getRoute()).toEqual({ page: "activity" });
  });

  it("keeps no copied Kata task presentation tree", () => {
    for (const path of removedPaths) {
      expect(existsSync(resolve(frontendRoot, path)), path).toBe(false);
    }
  });

  it("renders surviving task detail through Kata's shared package", () => {
    const panelImports = moduleImports("frontend/src/lib/components/kata/KataLinksPanel.svelte");
    expect(panelImports.some((specifier) => specifier.startsWith("@kenn-io/kata-ui/"))).toBe(true);

    const survivingIntegrationImports = [
      ...moduleImports("frontend/src/App.svelte"),
      ...moduleImports("frontend/src/lib/components/docs/DocsWorkspace.svelte"),
      ...moduleImports("frontend/src/lib/components/terminal/NewWorkspaceDialog.svelte"),
    ];
    expect(survivingIntegrationImports).not.toEqual(
      expect.arrayContaining([
        expect.stringMatching(/features\/kata|components\/kata|kata\/(?:snapshot|taskClient|taskTypes)/),
      ]),
    );
  });
});
