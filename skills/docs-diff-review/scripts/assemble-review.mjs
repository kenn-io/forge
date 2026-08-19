#!/usr/bin/env node

import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import { access, copyFile, cp, mkdir, readFile, readdir, stat, writeFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const scriptPath = fileURLToPath(import.meta.url);
const scriptDir = path.dirname(scriptPath);
const usage = `Usage: assemble-review.mjs --before-site <path> --after-site <path> --output <path> [options]

Options:
  --base <ref>       Comparison ref. Defaults to the merge base with origin/main.
  --repo-root <path> Repository used to resolve the default comparison ref.
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

function htmlPath(siteDir, pagePath) {
  return pagePath === "/"
    ? path.join(siteDir, "index.html")
    : path.join(siteDir, pagePath.slice(1), "index.html");
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

function referencedLocalPaths(html, pagePath) {
  const references = new Set();
  const base = new URL(pagePath, "https://docs.invalid");
  for (const tag of html.matchAll(/<([a-z0-9-]+)\b([^>]*)>/gi)) {
    const name = tag[1].toLowerCase();
    const attributes = tag[2];
    const src = attributes.match(/\bsrc\s*=\s*(["'])(.*?)\1/i)?.[2];
    const rel = attributes.match(/\brel\s*=\s*(["'])(.*?)\1/i)?.[2].toLowerCase() ?? "";
    const href = attributes.match(/\bhref\s*=\s*(["'])(.*?)\1/i)?.[2];
    const value = src ?? (name === "link" && /\b(stylesheet|icon)\b/.test(rel) ? href : undefined);
    if (value === undefined) continue;
    const decodedValue = value.replaceAll("&amp;", "&");
    if (!decodedValue || decodedValue.startsWith("#") || decodedValue.startsWith("//")) continue;
    let resolved;
    try {
      resolved = new URL(decodedValue, base);
    } catch {
      continue;
    }
    if (resolved.origin !== base.origin) continue;
    references.add(decodeURIComponent(resolved.pathname).replace(/^\/+/, ""));
  }
  return [...references].sort();
}

function comparablePageHtml(html, pagePath) {
  const article =
    html.match(
      /<article\b[^>]*class=["'][^"']*\bmd-content__inner\b[^"']*["'][^>]*>[\s\S]*?<\/article>/i,
    )?.[0] ?? html;
  if (pagePath !== "/") return article;
  const navigation =
    html.match(/<nav\b(?=[^>]*\bdata-md-level=["']0["'])[^>]*>[\s\S]*?<\/nav>/i)?.[0] ?? "";
  return `${article}\n${navigation}`;
}

async function renderedPageFingerprint(siteDir, pagePath) {
  const html = await readFile(htmlPath(siteDir, pagePath));
  const htmlText = html.toString("utf8");
  const hash = createHash("sha256").update(comparablePageHtml(htmlText, pagePath));
  for (const reference of referencedLocalPaths(htmlText, pagePath)) {
    const candidate = path.resolve(siteDir, reference);
    const relative = path.relative(siteDir, candidate);
    if (relative.startsWith("..") || path.isAbsolute(relative)) continue;
    let metadata;
    try {
      metadata = await stat(candidate);
    } catch {
      continue;
    }
    if (!metadata.isFile()) continue;
    hash.update(`\0${reference}\0`);
    hash.update(await readFile(candidate));
  }
  return hash.digest("hex");
}

async function discoverChangedPages(beforeSite, afterSite) {
  const candidates = [
    ...new Set([...(await discoverPages(beforeSite)), ...(await discoverPages(afterSite))]),
  ];
  const changed = [];
  for (const pagePath of candidates) {
    if (pagePath.startsWith("/adr/") || pagePath.startsWith("/reports/")) continue;
    const beforeAvailable = await exists(htmlPath(beforeSite, pagePath));
    const afterAvailable = await exists(htmlPath(afterSite, pagePath));
    if (!beforeAvailable || !afterAvailable) {
      changed.push(pagePath);
      continue;
    }
    const [beforeFingerprint, afterFingerprint] = await Promise.all([
      renderedPageFingerprint(beforeSite, pagePath),
      renderedPageFingerprint(afterSite, pagePath),
    ]);
    if (beforeFingerprint !== afterFingerprint) changed.push(pagePath);
  }
  return changed.sort((left, right) =>
    left === "/" ? -1 : right === "/" ? 1 : left.localeCompare(right),
  );
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

  const pagePaths = await discoverChangedPages(beforeSite, afterSite);

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
    copyFile(path.resolve(scriptDir, "../assets/viewer-utils.mjs"), path.join(output, "viewer-utils.mjs")),
  ]);
  await writeFile(path.join(output, "manifest.json"), `${JSON.stringify({ base, pages }, null, 2)}\n`);
  console.log(`Docs review assembled at ${output}`);
  console.log(`${pages.length} page${pages.length === 1 ? "" : "s"} compared against ${base}`);
}

if (path.resolve(process.argv[1] ?? "") === scriptPath) {
  main().catch((error) => {
    console.error(error instanceof Error ? error.message : error);
    process.exitCode = 1;
  });
}

export { discoverChangedPages, renderedPageFingerprint };
