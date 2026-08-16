import { describe, expect, it } from "vite-plus/test";

import { clipboardImageFiles, formatPastedImagePaths } from "./terminalImagePaste.js";

function clipboardData(items: readonly File[]): DataTransfer {
  return {
    items: items.map((file) => ({
      kind: "file",
      type: file.type,
      getAsFile: () => file,
    })),
    files: items,
  } as unknown as DataTransfer;
}

describe("terminal image paste helpers", () => {
  it("extracts every image file in clipboard order", () => {
    const png = new File(["png"], "first.png", { type: "image/png" });
    const text = new File(["text"], "note.txt", { type: "text/plain" });
    const jpeg = new File(["jpeg"], "second.jpg", { type: "image/jpeg" });
    const gif = new File(["gif"], "third.gif", { type: "image/gif" });
    const webp = new File(["webp"], "fourth.webp", { type: "image/webp" });
    const fifth = new File(["png"], "fifth.png", { type: "image/png" });

    expect(clipboardImageFiles(clipboardData([png, text, jpeg, gif, webp, fifth]))).toEqual([
      png,
      jpeg,
      gif,
      webp,
      fifth,
    ]);
  });

  it("falls back to clipboard files when items expose no image", () => {
    const png = new File(["png"], "first.png", { type: "image/png" });
    const data = {
      items: [{ kind: "string", type: "text/plain", getAsFile: () => null }],
      files: [png],
    } as unknown as DataTransfer;

    expect(clipboardImageFiles(data)).toEqual([png]);
  });

  it("treats text-only clipboard payloads without file collections as image-free", () => {
    expect(clipboardImageFiles({} as DataTransfer)).toEqual([]);
  });

  it("joins successful paths in source order and counts failures", () => {
    expect(
      formatPastedImagePaths([
        { _tag: "Success", path: "first.png" },
        { _tag: "Failure" },
        { _tag: "Success", path: "third.png" },
      ]),
    ).toEqual({ text: "first.png third.png", failed: 1 });
  });
});
