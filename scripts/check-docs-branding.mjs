import { readFile, readdir } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const scriptPath = fileURLToPath(import.meta.url);
const repoRoot = path.resolve(path.dirname(scriptPath), "..");
const rootDocs = ["README.md", "CLAUDE.md"];
const docsDirectories = ["docs", "context"];
const forbiddenBrand = "Kenn Forge";

async function markdownFilesUnder(root, relativeDir) {
  const absoluteDir = path.join(root, relativeDir);
  let entries;
  try {
    entries = await readdir(absoluteDir, { withFileTypes: true });
  } catch (error) {
    if (error?.code === "ENOENT") return [];
    throw error;
  }

  const files = [];
  for (const entry of entries) {
    const relativePath = path.join(relativeDir, entry.name);
    if (entry.isDirectory()) {
      files.push(...(await markdownFilesUnder(root, relativePath)));
    } else if (entry.isFile() && entry.name.endsWith(".md")) {
      files.push(relativePath);
    }
  }
  return files;
}

export async function findForbiddenDocsBranding(root = repoRoot) {
  const files = [
    ...rootDocs,
    ...(await Promise.all(docsDirectories.map((dir) => markdownFilesUnder(root, dir)))).flat(),
  ];
  const findings = [];

  for (const relativePath of files.sort()) {
    let source;
    try {
      source = await readFile(path.join(root, relativePath), "utf8");
    } catch (error) {
      if (error?.code === "ENOENT") continue;
      throw error;
    }
    for (const [index, line] of source.split("\n").entries()) {
      if (line.includes(forbiddenBrand)) {
        findings.push(`${relativePath.split(path.sep).join("/")}:${index + 1}: ${line}`);
      }
    }
  }
  return findings;
}

if (process.argv[1] && path.resolve(process.argv[1]) === scriptPath) {
  const findings = await findForbiddenDocsBranding();
  if (findings.length > 0) {
    console.error(`documentation must use kenn-forge, never ${forbiddenBrand}:`);
    for (const finding of findings) console.error(finding);
    process.exitCode = 1;
  }
}
