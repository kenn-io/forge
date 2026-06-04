const BRACKETED_PASTE_START = "\x1b[200~";
const BRACKETED_PASTE_END = "\x1b[201~";

export function isMultilinePaste(text: string): boolean {
  return text.includes("\n") || text.includes("\r");
}

export function createBracketedPastePayload(text: string): string {
  return `${BRACKETED_PASTE_START}${sanitizeTerminalPasteText(text)}${BRACKETED_PASTE_END}`;
}

export function createTerminalPastePayload(
  text: string,
  bracketedPasteMode: boolean,
): string {
  const sanitizedText = sanitizeTerminalPasteText(text);
  if (!bracketedPasteMode) return sanitizedText;
  return `${BRACKETED_PASTE_START}${sanitizedText}${BRACKETED_PASTE_END}`;
}

export function sanitizeTerminalPasteText(text: string): string {
  let sanitizedText = "";
  for (const character of text) {
    const codePoint = character.codePointAt(0);
    if (codePoint === undefined || isUnsafeTerminalControlByte(codePoint)) {
      continue;
    }
    sanitizedText += character;
  }
  return sanitizedText;
}

function isUnsafeTerminalControlByte(codePoint: number): boolean {
  return (
    codePoint <= 0x08 ||
    codePoint === 0x0b ||
    codePoint === 0x0c ||
    (codePoint >= 0x0e && codePoint <= 0x1f) ||
    (codePoint >= 0x7f && codePoint <= 0x9f)
  );
}
