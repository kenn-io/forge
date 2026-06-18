#!/usr/bin/env node

// Guard that the CI Playwright container image tag stays in lockstep with the
// pinned @playwright/test version. The e2e job runs inside the
// mcr.microsoft.com/playwright image, whose browsers are pre-baked and keyed to
// the image's Playwright version. If the image tag drifts from the npm pin,
// Playwright silently re-downloads browsers (losing the container's whole
// benefit) or runs mismatched binaries. This check fails the commit when they
// disagree, so the version coupling can never rot unnoticed.

import { readFile } from "node:fs/promises";
import { resolve } from "node:path";
import { fileURLToPath } from "node:url";

// package.json files that pin @playwright/test. All must agree, and the CI
// container image version must match them.
const PIN_FILES = ["frontend/package.json", "packages/github-app-ui/package.json"];
const WORKFLOW_FILE = ".github/workflows/ci.yml";
// A line that references the Playwright container image, by tag or by digest:
//   mcr.microsoft.com/playwright:v1.60.0-noble
//   mcr.microsoft.com/playwright@sha256:...  # v1.60.0-noble
const IMAGE_REF_RE = /mcr\.microsoft\.com\/playwright[:@]/;
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

  // Compare every Playwright container image reference in the workflow against
  // the pin. The image is digest-pinned, so the version is carried by the
  // trailing "# v<version>" comment, which must be present and must match.
  let workflow;
  try {
    workflow = await readFile(resolve(rootPath, WORKFLOW_FILE), "utf8");
  } catch (error) {
    findings.push({ file: WORKFLOW_FILE, message: `Unable to read ${WORKFLOW_FILE}: ${error.message}` });
    return findings;
  }

  workflow.split(/\r?\n/).forEach((line, index) => {
    if (!IMAGE_REF_RE.test(line)) return;

    const versionMatch = line.match(VERSION_RE);
    if (!versionMatch) {
      findings.push({
        file: WORKFLOW_FILE,
        line: index + 1,
        message:
          "Playwright container image has no v<version> tag or comment to verify. Add a " +
          "trailing '# v<version>' comment next to the digest so the pin stays traceable " +
          "and checkable against the @playwright/test pin.",
      });
      return;
    }

    const imageVersion = versionMatch[1];
    if (expected && imageVersion !== expected) {
      findings.push({
        file: WORKFLOW_FILE,
        line: index + 1,
        message:
          `Playwright container image is v${imageVersion} but @playwright/test is ${expected}. ` +
          `Update the digest pin and its '# v${expected}' comment so the pre-baked browsers match.`,
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
