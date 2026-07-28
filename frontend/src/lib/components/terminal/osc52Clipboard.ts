export const MAX_OSC52_CLIPBOARD_BYTES = 1024 * 1024;

export type Osc52ClipboardRejection =
  | "unsupported_selection"
  | "read_request"
  | "malformed"
  | "too_large"
  | "invalid_utf8";

export type Osc52ClipboardWriteResult =
  | { status: "accepted"; text: string }
  | { status: "rejected"; reason: Osc52ClipboardRejection };

const BASE64_PATTERN = /^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/;

export function parseOsc52ClipboardWrite(
  data: string,
  maxBytes = MAX_OSC52_CLIPBOARD_BYTES,
): Osc52ClipboardWriteResult {
  const separator = data.indexOf(";");
  if (separator < 0) return rejected("malformed");

  const selection = data.slice(0, separator);
  const payload = data.slice(separator + 1);
  if (selection !== "" && selection !== "c") {
    return rejected("unsupported_selection");
  }
  if (payload === "?") return rejected("read_request");

  const maximumEncodedLength = Math.ceil(maxBytes / 3) * 4;
  if (payload.length > maximumEncodedLength) return rejected("too_large");
  if (!BASE64_PATTERN.test(payload)) return rejected("malformed");

  let encodedBytes: string;
  try {
    encodedBytes = atob(payload);
  } catch {
    return rejected("malformed");
  }
  if (encodedBytes.length > maxBytes) return rejected("too_large");

  const bytes = Uint8Array.from(encodedBytes, (character) => character.charCodeAt(0));
  try {
    return {
      status: "accepted",
      text: new TextDecoder("utf-8", { fatal: true }).decode(bytes),
    };
  } catch {
    return rejected("invalid_utf8");
  }
}

function rejected(reason: Osc52ClipboardRejection): Osc52ClipboardWriteResult {
  return { status: "rejected", reason };
}
