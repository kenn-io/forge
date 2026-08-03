import { cp, mkdir, rm, writeFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { buildDocs } from "./build-docs.mjs";

const scriptPath = fileURLToPath(import.meta.url);
const repoRoot = path.resolve(path.dirname(scriptPath), "..");

const outputConfig = {
  version: 3,
  routes: [{ src: "/", dest: "/index.html" }, { handle: "filesystem" }, { src: "/(.+)/", dest: "/$1/index.html" }],
};

export async function stageDocsDeployment(siteDir, outputDir) {
  await rm(outputDir, { recursive: true, force: true });
  await mkdir(outputDir, { recursive: true });
  await cp(siteDir, path.join(outputDir, "static"), { recursive: true });
  await writeFile(path.join(outputDir, "config.json"), `${JSON.stringify(outputConfig, null, 2)}\n`);
}

export async function prepareDocsDeployment() {
  const siteDir = path.join(repoRoot, "site");
  const outputDir = path.join(repoRoot, ".vercel", "output");

  await buildDocs();
  await stageDocsDeployment(siteDir, outputDir);
  console.log(`Vercel deployment prepared at ${outputDir}`);
}

if (process.argv[1] && path.resolve(process.argv[1]) === scriptPath) {
  prepareDocsDeployment().catch((error) => {
    console.error(error instanceof Error ? error.message : error);
    process.exitCode = 1;
  });
}
