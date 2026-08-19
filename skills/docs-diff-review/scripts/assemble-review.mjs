#!/usr/bin/env node

import { execFileSync } from "node:child_process";
import { access, copyFile, cp, mkdir, readFile, readdir, writeFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const usage = `Usage: assemble-review.mjs --before-site <path> --after-site <path> --output <path> [options]

Options:
  --base <ref>       Comparison ref. Defaults to the merge base with origin/main.
  --repo-root <path> Repository used to discover changed documentation files.
  --help             Show this help.`;

function parseArgs(argv) {
  if (argv.includes("--help") || argv.includes("-h")) return null;
  const values = new Map();
  for (let index = 0; index < argv.length; index += 2) {
    const name = argv[index];
    const value = argv[index + 1];
    if (!name?.startsWith("--") || !value) throw new Error(`Expected a value after ${name ?? "argument"}`);
    values.set(name.slice(2), value);
  }
  return values;
}

function required(args, name) {
  const value = args.get(name);
  if (!value) throw new Error(`--${name} is required`);
  return value;
}

function git(repoRoot, args) {
  return execFileSync("git", args, { cwd: repoRoot, encoding: "utf8" }).trim();
}

function pagePathForDoc(file) {
  if (!file.startsWith("docs/") || !file.endsWith(".md")) return null;
  const relative = file.slice("docs/".length, -".md".length);
  if (relative === "index") return "/";
  if (relative.endsWith("/index")) return `/${relative.slice(0, -"/index".length)}/`;
  return `/${relative}/`;
}

function htmlPath(siteDir, pagePath) {
  return pagePath === "/" ? path.join(siteDir, "index.html") : path.join(siteDir, pagePath.slice(1), "index.html");
}

async function exists(candidate) {
  try {
    await access(candidate);
    return true;
  } catch {
    return false;
  }
}

async function discoverPages(siteDir, current = siteDir) {
  const pages = [];
  for (const entry of await readdir(current, { withFileTypes: true })) {
    if (entry.name === "assets" || entry.name === "review") continue;
    const candidate = path.join(current, entry.name);
    if (entry.isDirectory()) pages.push(...(await discoverPages(siteDir, candidate)));
    if (entry.isFile() && entry.name === "index.html") {
      const relative = path.relative(siteDir, current);
      pages.push(relative ? `/${relative.split(path.sep).join("/")}/` : "/");
    }
  }
  return pages;
}

function decodeText(value) {
  return value
    .replace(/<[^>]+>/g, " ")
    .replaceAll("&amp;", "&")
    .replaceAll("&lt;", "<")
    .replaceAll("&gt;", ">")
    .replaceAll("&#39;", "'")
    .replaceAll("&quot;", '"')
    .replaceAll("&para;", "")
    .replaceAll("¶", "")
    .replace(/\s+/g, " ")
    .trim();
}

async function pageTitle(pagePath, beforeSite, afterSite) {
  for (const siteDir of [afterSite, beforeSite]) {
    const candidate = htmlPath(siteDir, pagePath);
    if (!(await exists(candidate))) continue;
    const html = await readFile(candidate, "utf8");
    const heading = html.match(/<article\b[^>]*>[\s\S]*?<h1\b[^>]*>([\s\S]*?)<\/h1>/i)?.[1];
    if (heading) return decodeText(heading);
  }
  return pagePath === "/" ? "Overview" : pagePath.split("/").filter(Boolean).at(-1).replaceAll("-", " ");
}

async function main() {
  const args = parseArgs(process.argv.slice(2));
  if (args === null) {
    console.log(usage);
    return;
  }
  const repoRoot = path.resolve(args.get("repo-root") ?? process.cwd());
  const beforeSite = path.resolve(required(args, "before-site"));
  const afterSite = path.resolve(required(args, "after-site"));
  const output = path.resolve(required(args, "output"));
  const base = args.get("base") ?? git(repoRoot, ["merge-base", "HEAD", "origin/main"]);

  for (const siteDir of [beforeSite, afterSite]) {
    if (!(await exists(path.join(siteDir, "index.html")))) throw new Error(`Rendered site not found at ${siteDir}`);
  }
  if (await exists(output)) throw new Error(`Output already exists: ${output}`);

  const tracked = git(repoRoot, ["diff", "--name-only", base, "--", "docs"]).split("\n").filter(Boolean);
  const untracked = git(repoRoot, ["ls-files", "--others", "--exclude-standard", "docs"]).split("\n").filter(Boolean);
  const changedFiles = [...new Set([...tracked, ...untracked])];
  const structuralChange = changedFiles.some((file) => !file.endsWith(".md"));

  let pagePaths;
  if (structuralChange) {
    pagePaths = [...new Set([...(await discoverPages(beforeSite)), ...(await discoverPages(afterSite))])];
  } else {
    pagePaths = changedFiles.map(pagePathForDoc).filter(Boolean);
  }
  pagePaths = pagePaths
    .filter((pagePath) => !pagePath.startsWith("/adr/") && !pagePath.startsWith("/reports/"))
    .sort((left, right) => (left === "/" ? -1 : right === "/" ? 1 : left.localeCompare(right)));

  const pages = [];
  for (const pagePath of pagePaths) {
    const beforeAvailable = await exists(htmlPath(beforeSite, pagePath));
    const afterAvailable = await exists(htmlPath(afterSite, pagePath));
    if (!beforeAvailable && !afterAvailable) continue;
    pages.push({
      label: await pageTitle(pagePath, beforeSite, afterSite),
      path: pagePath,
      beforeAvailable,
      afterAvailable,
    });
  }
  if (pages.length === 0) throw new Error(`No changed rendered documentation pages found against ${base}`);

  await mkdir(path.dirname(output), { recursive: true });
  await mkdir(output);
  await Promise.all([
    cp(beforeSite, path.join(output, "before"), { recursive: true }),
    cp(afterSite, path.join(output, "after"), { recursive: true }),
    copyFile(path.resolve(scriptDir, "../assets/index.html"), path.join(output, "index.html")),
  ]);
  await writeFile(path.join(output, "manifest.json"), `${JSON.stringify({ base, pages }, null, 2)}\n`);
  console.log(`Docs review assembled at ${output}`);
  console.log(`${pages.length} page${pages.length === 1 ? "" : "s"} compared against ${base}`);
}

main().catch((error) => {
  console.error(error instanceof Error ? error.message : error);
  process.exitCode = 1;
});
