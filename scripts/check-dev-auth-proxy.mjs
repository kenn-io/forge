#!/usr/bin/env node
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { fileURLToPath } from "node:url";
import ts from "typescript";

// Bootstrap redirects cross from the backend base_path into Vite's root
// namespace. Keep that translation in the tested helper, not inline options.
export function lintDevAuthProxy(source) {
  const file = ts.createSourceFile("vite.config.ts", source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS);
  const imports = new Set();
  for (const statement of file.statements) {
    if (
      !ts.isImportDeclaration(statement) ||
      !ts.isStringLiteral(statement.moduleSpecifier) ||
      statement.moduleSpecifier.text !== "./src/lib/dev/authBootstrapProxy.ts"
    )
      continue;
    const bindings = statement.importClause?.namedBindings;
    if (!bindings || !ts.isNamedImports(bindings)) continue;
    for (const binding of bindings.elements) {
      if ((binding.propertyName ?? binding.name).text === "authBootstrapProxy") imports.add(binding.name.text);
    }
  }
  const findings = [];
  let found = false;
  function visit(node) {
    if (ts.isPropertyAssignment(node) && ts.isStringLiteral(node.name) && node.name.text.includes("auth_token")) {
      found = true;
      const value = node.initializer;
      if (!ts.isCallExpression(value) || !ts.isIdentifier(value.expression) || !imports.has(value.expression.text)) {
        const { line } = file.getLineAndCharacterOfPosition(node.getStart(file));
        findings.push(
          `frontend/vite.config.ts:${line + 1}: Auth bootstrap proxy must use authBootstrapProxy from ./src/lib/dev/authBootstrapProxy.ts; inline proxies leak backend base_path into browser redirects.`,
        );
      }
    }
    ts.forEachChild(node, visit);
  }
  visit(file);
  if (!found)
    findings.push(
      "frontend/vite.config.ts: Missing auth_token proxy through authBootstrapProxy; dev browser login requires backend cookie bootstrap.",
    );
  return findings;
}

if (resolve(process.argv[1] ?? "") === fileURLToPath(import.meta.url)) {
  const findings = lintDevAuthProxy(readFileSync("frontend/vite.config.ts", "utf8"));
  for (const finding of findings) console.error(finding);
  if (findings.length) process.exitCode = 1;
}
