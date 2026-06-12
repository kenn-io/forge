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
  });

  it("keeps binary files outside the code bucket", () => {
    expect(categorizeDiffFile(binaryFile("assets/logo.png"))).toBe("other");
    expect(categorizeDiffFile(binaryFile("release/middleman.tar.gz"))).toBe("other");
  });
});
