import { readFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { publishedMarkdownPages } from "./build-docs.mjs";

const scriptPath = fileURLToPath(import.meta.url);
const repoRoot = path.resolve(path.dirname(scriptPath), "..");

const bareBinaryName = /(?<![\w/-])kenn-forge(?![\w-])/;
const fenceMarker = /^\s*(```|~~~)/;

export async function findForbiddenDocsBranding(root = repoRoot) {
  const findings = [];

  for (const page of publishedMarkdownPages()) {
    const relativePath = path.join("docs", page);
    let source;
    try {
      source = await readFile(path.join(root, relativePath), "utf8");
    } catch (error) {
      if (error?.code === "ENOENT") continue;
      throw error;
    }

    let inFence = false;
    for (const [index, line] of source.split("\n").entries()) {
      if (fenceMarker.test(line)) {
        inFence = !inFence;
        continue;
      }
      if (inFence) continue;
      const prose = line
        .split(/(`[^`]*`)/)
        .filter((segment) => !segment.startsWith("`"))
        .join("");
      if (bareBinaryName.test(prose)) {
        findings.push(`${relativePath.split(path.sep).join("/")}:${index + 1}: ${line}`);
      }
    }
  }
  return findings;
}

if (process.argv[1] && path.resolve(process.argv[1]) === scriptPath) {
  const findings = await findForbiddenDocsBranding();
  if (findings.length > 0) {
    console.error(
      "docs prose must introduce the product as Kenn Forge and then say Forge; " +
        "reserve kenn-forge for the binary, CLI, and paths inside code spans:",
    );
    for (const finding of findings) console.error(finding);
    process.exitCode = 1;
  }
}
