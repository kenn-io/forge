import { describe, expect, it } from "vitest";

import { createBracketedPastePayload, isMultilinePaste } from "./bracketedPaste";

describe("bracketed paste payloads", () => {
  it("detects line-feed and carriage-return multiline paste text", () => {
    expect(isMultilinePaste("single line")).toBe(false);
    expect(isMultilinePaste("one\ntwo")).toBe(true);
    expect(isMultilinePaste("one\rtwo")).toBe(true);
  });

  it("wraps pasted text without normalizing literal newlines", () => {
    expect(createBracketedPastePayload("one\r\ntwo\nthree")).toBe(
      "\x1b[200~one\r\ntwo\nthree\x1b[201~",
    );
  });
});
