import type { ChatMessage } from "./chat-types.js";

/**
 * Prompts a person sent in this chat, oldest first, as far back as the
 * transcript is loaded. Coordinator prompts are not theirs to recall.
 */
export function sentPrompts(messages: readonly ChatMessage[]): ChatMessage[] {
  return messages.filter(
    (message) => message.role === "user" && message.sender?.kind !== "coordinator" && message.text.trim(),
  );
}

/**
 * Whether the collapsed caret sits on the field's first or last visual line,
 * counting wrapped lines. A textarea exposes no caret geometry, so this lays
 * the text out in a hidden copy of the field and compares the line tops of a
 * marker at the caret and one at that edge.
 */
export function caretOnEdgeLine(field: HTMLTextAreaElement, edge: "first" | "last"): boolean {
  const { selectionStart, selectionEnd, value } = field;
  if (selectionStart !== selectionEnd) return false;
  const mirror = document.createElement("div");
  const style = getComputedStyle(field);
  for (const name of style) mirror.style.setProperty(name, style.getPropertyValue(name));
  Object.assign(mirror.style, {
    display: "block",
    position: "absolute",
    top: "0",
    left: "-9999px",
    visibility: "hidden",
    overflow: "hidden",
    height: "auto",
    minHeight: "0",
    maxHeight: "none",
    border: "0",
    boxSizing: "border-box",
    width: `${field.clientWidth}px`,
    whiteSpace: "pre-wrap",
    overflowWrap: "break-word",
  });
  // Each marker wraps a character, since Firefox places an empty span at a
  // different height. At a soft wrap the field draws the caret at the start
  // of the next line, so the caret takes the line of the character after it.
  // Only the end marker adds text: a zero-width space, which also opens the
  // line after a trailing newline.
  const marker = (text: string) => Object.assign(document.createElement("span"), { textContent: text });
  const charAt = (index: number) => String.fromCodePoint(value.codePointAt(index)!);
  const caretChar = selectionStart < value.length ? charAt(selectionStart) : "\u200b";
  const caret = marker(caretChar);
  const firstChar = selectionStart === 0 ? caretChar : charAt(0);
  const first = selectionStart === 0 ? caret : marker(firstChar);
  const last = selectionStart === value.length ? caret : marker("\u200b");
  if (first !== caret) mirror.append(first, value.slice(firstChar.length, selectionStart));
  mirror.append(caret, value.slice(selectionStart + caretChar.length));
  if (last !== caret) mirror.append(last);
  document.body.append(mirror);
  try {
    const top = caret.getBoundingClientRect().top;
    const edgeTop = (edge === "first" ? first : last).getBoundingClientRect().top;
    return Math.abs(top - edgeTop) < 1;
  } finally {
    mirror.remove();
  }
}
