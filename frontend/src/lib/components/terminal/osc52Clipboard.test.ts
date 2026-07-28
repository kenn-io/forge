import { describe, expect, it } from "vite-plus/test";
import { MAX_OSC52_CLIPBOARD_BYTES, parseOsc52ClipboardWrite } from "./osc52Clipboard";

function encode(text: string): string {
  const bytes = new TextEncoder().encode(text);
  let binary = "";
  for (let offset = 0; offset < bytes.length; offset += 0x8000) {
    binary += String.fromCharCode(...bytes.subarray(offset, offset + 0x8000));
  }
  return btoa(binary);
}

describe("OSC 52 clipboard writes", () => {
  it.each(["c", ""])("decodes the %s clipboard target", (target) => {
    expect(parseOsc52ClipboardWrite(`${target};${encode("copied text")}`)).toEqual({
      status: "accepted",
      text: "copied text",
    });
  });

  it("accepts an empty clipboard write", () => {
    expect(parseOsc52ClipboardWrite("c;")).toEqual({
      status: "accepted",
      text: "",
    });
  });

  it.each([
    ["p;YQ==", "unsupported_selection"],
    ["c;?", "read_request"],
    ["missing-separator", "malformed"],
    ["c;%%%", "malformed"],
    ["c;/w==", "invalid_utf8"],
  ] as const)("rejects %s as %s", (data, reason) => {
    expect(parseOsc52ClipboardWrite(data)).toEqual({
      status: "rejected",
      reason,
    });
  });

  it("rejects decoded clipboard data over the limit", () => {
    const payload = encode("x".repeat(MAX_OSC52_CLIPBOARD_BYTES + 1));
    expect(parseOsc52ClipboardWrite(`c;${payload}`)).toEqual({
      status: "rejected",
      reason: "too_large",
    });
  });
});
