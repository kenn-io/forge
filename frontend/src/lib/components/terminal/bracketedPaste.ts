const BRACKETED_PASTE_START = "\x1b[200~";
const BRACKETED_PASTE_END = "\x1b[201~";

export function isMultilinePaste(text: string): boolean {
  return text.includes("\n") || text.includes("\r");
}

export function createBracketedPastePayload(text: string): string {
  return `${BRACKETED_PASTE_START}${text}${BRACKETED_PASTE_END}`;
}
