#!/usr/bin/env node

// Guard that the browser CI image stays in lockstep with the repository's
// Playwright, Bun, and Vite+ pins. The browser jobs run inside a
// repository-owned image derived from mcr.microsoft.com/playwright.

import { readFile } from "node:fs/promises";
import { resolve } from "node:path";
import { fileURLToPath } from "node:url";

// package.json files that pin @playwright/test. All must agree, and the CI
// image recipe's base Playwright version must match them.
const PIN_FILES = ["package.json", "frontend/package.json", "packages/github-app-ui/package.json"];
const IMAGE_FILE = ".github/docker/playwright/Dockerfile";
// A line that references the Playwright container image, by tag or by digest:
//   mcr.microsoft.com/playwright:v1.60.0-noble
//   mcr.microsoft.com/playwright@sha256:...  # v1.60.0-noble
const IMAGE_REF_RE = /mcr\.microsoft\.com\/playwright[:@]/;
const BUN_IMAGE_REF_RE = /oven\/bun:(\d+\.\d+\.\d+)@sha256:[a-f0-9]{64}/;
const VITE_PLUS_VERSION_RE = /^ARG VITE_PLUS_VERSION=(\d+\.\d+\.\d+)$/;
// The version lives either in the tag (:v1.60.0-noble) or, for a digest pin, in
// the trailing "# v1.60.0-noble" comment. Either way it is the v<semver> token;
// a 64-hex sha256 digest carries no such token, so it cannot match by accident.
const VERSION_RE = /v(\d+\.\d+\.\d+)/;

function pinnedPlaywright(pkg) {
  return pkg.devDependencies?.["@playwright/test"] ?? pkg.dependencies?.["@playwright/test"];
}

export async function checkPlaywrightVersion({ root = process.cwd() } = {}) {
  const rootPath = resolve(root);
  const findings = [];
  let rootPackage;

  // Collect the pinned @playwright/test version from each package.json.
  const pins = new Map();
  for (const rel of PIN_FILES) {
    let pkg;
    try {
      pkg = JSON.parse(await readFile(resolve(rootPath, rel), "utf8"));
    } catch (error) {
      findings.push({ file: rel, message: `Unable to read ${rel}: ${error.message}` });
      continue;
    }
    const version = pinnedPlaywright(pkg);
    if (version) pins.set(rel, version);
    if (rel === "package.json") rootPackage = pkg;
  }

  // Every package.json must pin the same version.
  const distinct = new Set(pins.values());
  if (distinct.size > 1) {
    const detail = [...pins].map(([file, version]) => `${file}=${version}`).join(", ");
    findings.push({
      file: PIN_FILES.join(", "),
      message: `Conflicting @playwright/test pins (${detail}); they must match.`,
    });
  }

  const expected = pins.size > 0 ? [...pins.values()][0] : null;

  // Compare every Playwright base image reference in the image recipe against
  // the pin. A tag plus digest keeps the human-readable version and immutable
  // content identity in one reference.
  let imageFile;
  try {
    imageFile = await readFile(resolve(rootPath, IMAGE_FILE), "utf8");
  } catch (error) {
    findings.push({ file: IMAGE_FILE, message: `Unable to read ${IMAGE_FILE}: ${error.message}` });
    return findings;
  }

  imageFile.split(/\r?\n/).forEach((line, index) => {
    if (IMAGE_REF_RE.test(line)) {
      const versionMatch = line.match(VERSION_RE);
      if (!versionMatch) {
        findings.push({
          file: IMAGE_FILE,
          line: index + 1,
          message:
            "Playwright base image has no v<version> tag to verify. Keep the readable tag " +
            "alongside the digest so the pin remains checkable against @playwright/test.",
        });
      } else {
        const imageVersion = versionMatch[1];
        if (expected && imageVersion !== expected) {
          findings.push({
            file: IMAGE_FILE,
            line: index + 1,
            message:
              `Playwright base image is v${imageVersion} but @playwright/test is ${expected}. ` +
              `Update its tag and digest so the pre-baked browsers match.`,
          });
        }
      }
    }

    const bunImageMatch = line.match(BUN_IMAGE_REF_RE);
    const packageManagerMatch = rootPackage?.packageManager?.match(/^bun@(\d+\.\d+\.\d+)$/);
    if (bunImageMatch && packageManagerMatch && bunImageMatch[1] !== packageManagerMatch[1]) {
      findings.push({
        file: IMAGE_FILE,
        line: index + 1,
        message:
          `Bun image is ${bunImageMatch[1]} but packageManager is bun@${packageManagerMatch[1]}. ` +
          "Update its tag and digest together.",
      });
    }

    const vitePlusImageMatch = line.match(VITE_PLUS_VERSION_RE);
    const vitePlusPin = rootPackage?.devDependencies?.["vite-plus"];
    if (vitePlusImageMatch && vitePlusPin && vitePlusImageMatch[1] !== vitePlusPin) {
      findings.push({
        file: IMAGE_FILE,
        line: index + 1,
        message:
          `Baked Vite+ is ${vitePlusImageMatch[1]} but package.json pins vite-plus ${vitePlusPin}. ` +
          "Update them together.",
      });
    }
  });

  return findings;
}

async function main() {
  const findings = await checkPlaywrightVersion();
  if (findings.length === 0) return;

  for (const finding of findings) {
    const where = finding.line ? `${finding.file}:${finding.line}` : finding.file;
    console.error(`${where}: ${finding.message}`);
  }
  process.exitCode = 1;
}

const currentFile = fileURLToPath(import.meta.url);
if (resolve(process.argv[1] ?? "") === currentFile) {
  main().catch((error) => {
    console.error(error instanceof Error ? error.message : error);
    process.exitCode = 1;
  });
}
