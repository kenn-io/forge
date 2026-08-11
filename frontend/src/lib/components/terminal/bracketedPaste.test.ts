import { describe, expect, it } from "vite-plus/test";

import {
  createBracketedPastePayload,
  createTerminalPastePayload,
  isMultilinePaste,
  sanitizeTerminalPasteText,
} from "./bracketedPaste";

describe("bracketed paste payloads", () => {
  it("detects line-feed and carriage-return multiline paste text", () => {
    expect(isMultilinePaste("single line")).toBe(false);
    expect(isMultilinePaste("one\ntwo")).toBe(true);
    expect(isMultilinePaste("one\rtwo")).toBe(true);
  });

  it("normalizes Windows and Unix line endings like xterm", () => {
    expect(createBracketedPastePayload("one\r\ntwo\nthree\rfour")).toBe("\x1b[200~one\rtwo\rthree\rfour\x1b[201~");
  });

  it("strips terminal control bytes while preserving tabs and normalized line endings", () => {
    expect(sanitizeTerminalPasteText("one\t\n\x1b[201~\nmalicious-command\r\n\x9b200~\x07two")).toBe(
      "one\t\r[201~\rmalicious-command\r200~two",
    );
  });

  it("sanitizes pasted text before building bracketed paste payloads", () => {
    expect(createBracketedPastePayload("one\x1b[201~\ntwo")).toBe("\x1b[200~one[201~\rtwo\x1b[201~");
  });

  it("builds raw sanitized payloads when bracketed paste mode is disabled", () => {
    expect(createTerminalPastePayload("one\x1b[201~\ntwo", false)).toBe("one[201~\rtwo");
  });
});
