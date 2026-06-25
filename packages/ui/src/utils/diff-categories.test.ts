import { describe, expect, it } from "vite-plus/test";
import type { DiffFile } from "../api/types.js";
import { categorizeDiffFile } from "./diff-categories.js";

function file(path: string): DiffFile {
  return {
    path,
    old_path: path,
    status: "modified",
    is_binary: false,
    is_whitespace_only: false,
    additions: 1,
    deletions: 1,
    patch: "@@ -1 +1 @@\n-old\n+new\n",
    hunks: [],
  };
}

function binaryFile(path: string): DiffFile {
  return {
    ...file(path),
    is_binary: true,
    patch: "",
  };
}

describe("diff file categorization", () => {
  it("treats non-binary changed files as code without per-language allowlisting", () => {
    expect(categorizeDiffFile(file("flake.nix"))).toBe("code");
    expect(categorizeDiffFile(file("config/middleman.toml"))).toBe("code");
    expect(categorizeDiffFile(file("Makefile"))).toBe("code");
    expect(categorizeDiffFile(file("docs/scripts/bootstrap.sh"))).toBe("code");
    expect(categorizeDiffFile(file("docs/screenshots/playwright.config.ts"))).toBe("code");
  });

  it("treats lockfiles as generated", () => {
    expect(categorizeDiffFile(file("flake.lock"))).toBe("generated");
    expect(categorizeDiffFile(file("pixi.lock"))).toBe("generated");
    expect(categorizeDiffFile(file("pnpm-lock.yaml"))).toBe("generated");
    expect(categorizeDiffFile(file("package-lock.json"))).toBe("generated");
    expect(categorizeDiffFile(binaryFile("flake.lock"))).toBe("generated");
  });

  it("keeps non-generated binary files outside the code bucket", () => {
    expect(categorizeDiffFile(binaryFile("assets/logo.png"))).toBe("other");
    expect(categorizeDiffFile(binaryFile("docs/logo.png"))).toBe("other");
    expect(categorizeDiffFile(binaryFile("tests/fixtures/screenshot.png"))).toBe("other");
    expect(categorizeDiffFile(binaryFile("release/middleman.tar.gz"))).toBe("other");
  });
});
