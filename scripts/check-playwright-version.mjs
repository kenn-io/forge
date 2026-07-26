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
const WORKFLOW_FILE = ".github/workflows/ci.yml";
// A line that references the Playwright container image, by tag or by digest:
//   mcr.microsoft.com/playwright:v1.60.0-noble
//   mcr.microsoft.com/playwright@sha256:...  # v1.60.0-noble
const IMAGE_REF_RE = /mcr\.microsoft\.com\/playwright[:@]/;
const BUN_IMAGE_REF_RE = /^ARG BUN_IMAGE=oven\/bun:(\d+\.\d+\.\d+)@sha256:[a-f0-9]{64}$/;
const VITE_PLUS_VERSION_RE = /^ARG VITE_PLUS_VERSION=(\d+\.\d+\.\d+)$/;
const EXACT_VERSION_RE = /^\d+\.\d+\.\d+$/;
const USES_RE = /^\s*(?:-\s*)?uses:\s*(?:"([^"]+)"|'([^']+)'|([^\s#]+))(?:\s+#.*)?$/;
// The version lives either in the tag (:v1.60.0-noble) or, for a digest pin, in
// the trailing "# v1.60.0-noble" comment. Either way it is the v<semver> token;
// a 64-hex sha256 digest carries no such token, so it cannot match by accident.
const VERSION_RE = /v(\d+\.\d+\.\d+)/;

function pinnedPlaywright(pkg) {
  return pkg.devDependencies?.["@playwright/test"] ?? pkg.dependencies?.["@playwright/test"];
}

function requiredArgMatch(lines, name, pattern, invalidMessage, findings) {
  const declarations = lines
    .map((line, index) => ({ line, lineNumber: index + 1 }))
    .filter(({ line }) => line.startsWith(`ARG ${name}=`));
  if (declarations.length !== 1) {
    findings.push({
      file: IMAGE_FILE,
      message: `Dockerfile must define exactly one ARG ${name} declaration.`,
    });
    return null;
  }

  const declaration = declarations[0];
  const match = declaration.line.match(pattern);
  if (!match) {
    findings.push({ file: IMAGE_FILE, line: declaration.lineNumber, message: invalidMessage });
    return null;
  }
  return { match, lineNumber: declaration.lineNumber };
}

function indentation(line) {
  return line.length - line.trimStart().length;
}

function isSetupVpUse(line) {
  const match = line.match(USES_RE);
  const action = match?.[1] ?? match?.[2] ?? match?.[3];
  return action?.startsWith("voidzero-dev/setup-vp@");
}

function setupVpVersion(lines, actionIndex) {
  const actionIndent = indentation(lines[actionIndex]);
  let stepIndent = actionIndent;
  for (let index = actionIndex; index >= 0; index -= 1) {
    const line = lines[index];
    if (/^\s*-\s/.test(line) && indentation(line) <= actionIndent) {
      stepIndent = indentation(line);
      break;
    }
  }

  let withIndent = null;
  let withEntryIndent = null;
  for (let index = actionIndex + 1; index < lines.length; index += 1) {
    const line = lines[index];
    const lineIndent = indentation(line);
    if (index > actionIndex && /^\s*-\s/.test(line) && lineIndent <= stepIndent) break;

    if (withIndent === null) {
      if (/^\s*with:\s*(?:#.*)?$/.test(line)) withIndent = lineIndent;
      continue;
    }
    if (line.trim() !== "" && lineIndent <= withIndent) break;
    if (line.trimStart().startsWith("#")) continue;

    if (withEntryIndent === null) withEntryIndent = lineIndent;
    if (lineIndent !== withEntryIndent) continue;

    const versionMatch = line.match(/^\s*version:\s*(.*?)\s*(?:#.*)?$/);
    if (versionMatch && lineIndent > withIndent) {
      const value = versionMatch[1].replace(/^(["'])(.*)\1$/, "$2");
      return { line: index + 1, value };
    }
  }

  return null;
}

function checkSetupVpVersions(lines, expected, findings) {
  lines.forEach((line, index) => {
    if (!isSetupVpUse(line)) return;

    const version = setupVpVersion(lines, index);
    if (!version) {
      findings.push({
        file: WORKFLOW_FILE,
        line: index + 1,
        message: "setup-vp must set with.version to the exact package.json vite-plus pin.",
      });
      return;
    }
    if (!EXACT_VERSION_RE.test(version.value)) {
      findings.push({
        file: WORKFLOW_FILE,
        line: version.line,
        message: "setup-vp version must be an exact x.y.z value.",
      });
      return;
    }
    if (expected && version.value !== expected) {
      findings.push({
        file: WORKFLOW_FILE,
        line: version.line,
        message: `setup-vp version is ${version.value} but package.json pins vite-plus ${expected}.`,
      });
    }
  });
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

  const imageLines = imageFile.split(/\r?\n/);
  imageLines.forEach((line, index) => {
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
  });

  const packageManagerMatch = rootPackage?.packageManager?.match(/^bun@(\d+\.\d+\.\d+)$/);
  if (!packageManagerMatch) {
    findings.push({
      file: "package.json",
      message: "packageManager must pin Bun as bun@<version>.",
    });
  }

  const bunImageMatch = requiredArgMatch(
    imageLines,
    "BUN_IMAGE",
    BUN_IMAGE_REF_RE,
    "BUN_IMAGE must pin oven/bun as <version>@sha256:<digest>.",
    findings,
  );
  if (bunImageMatch && packageManagerMatch && bunImageMatch.match[1] !== packageManagerMatch[1]) {
    findings.push({
      file: IMAGE_FILE,
      line: bunImageMatch.lineNumber,
      message:
        `Bun image is ${bunImageMatch.match[1]} but packageManager is bun@${packageManagerMatch[1]}. ` +
        "Update its tag and digest together.",
    });
  }

  const vitePlusPin = rootPackage?.devDependencies?.["vite-plus"];
  const vitePlusPinMatch = vitePlusPin?.match(/^(\d+\.\d+\.\d+)$/);
  if (!vitePlusPinMatch) {
    findings.push({
      file: "package.json",
      message: "devDependencies.vite-plus must use an exact version.",
    });
  }

  const vitePlusImageMatch = requiredArgMatch(
    imageLines,
    "VITE_PLUS_VERSION",
    VITE_PLUS_VERSION_RE,
    "VITE_PLUS_VERSION must use an exact version.",
    findings,
  );
  if (vitePlusImageMatch && vitePlusPinMatch && vitePlusImageMatch.match[1] !== vitePlusPinMatch[1]) {
    findings.push({
      file: IMAGE_FILE,
      line: vitePlusImageMatch.lineNumber,
      message:
        `Baked Vite+ is ${vitePlusImageMatch.match[1]} but package.json pins vite-plus ${vitePlusPinMatch[1]}. ` +
        "Update them together.",
    });
  }

  let workflowFile;
  try {
    workflowFile = await readFile(resolve(rootPath, WORKFLOW_FILE), "utf8");
  } catch (error) {
    findings.push({ file: WORKFLOW_FILE, message: `Unable to read ${WORKFLOW_FILE}: ${error.message}` });
    return findings;
  }
  checkSetupVpVersions(workflowFile.split(/\r?\n/), vitePlusPinMatch?.[1], findings);

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
