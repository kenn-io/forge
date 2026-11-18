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
  // Empty markers add no break opportunity, so the copy wraps as the field
  // does. The end marker holds a zero-width space so a trailing newline still
  // opens the line the caret would sit on.
  const start = document.createElement("span");
  const caret = document.createElement("span");
  const end = document.createElement("span");
  end.textContent = "\u200b";
  mirror.append(start, value.slice(0, selectionStart), caret, value.slice(selectionStart), end);
  document.body.append(mirror);
  try {
    const top = caret.getBoundingClientRect().top;
    const edgeTop = (edge === "first" ? start : end).getBoundingClientRect().top;
    return Math.abs(top - edgeTop) < 1;
  } finally {
    mirror.remove();
  }
}
