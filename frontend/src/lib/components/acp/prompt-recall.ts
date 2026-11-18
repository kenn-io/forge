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
  // different height. At a soft wrap one caret offset is drawn either at the
  // end of the upper line (after End) or the start of the lower one (after
  // Home), and the field does not say which. So the caret counts as on an
  // edge line only if both places are: the first line takes the character
  // after the caret, and the last line the one before it, unless that is a
  // newline. Otherwise the arrow moves the caret as usual. The end marker is
  // a zero-width space, which also opens the line after a trailing newline.
  const marker = (text: string) => Object.assign(document.createElement("span"), { textContent: text });
  let from = selectionStart;
  let to = selectionStart;
  if (edge === "last" && selectionStart > 0 && value[selectionStart - 1] !== "\n") {
    from -= /[\uDC00-\uDFFF]/.test(value[selectionStart - 1]!) && selectionStart > 1 ? 2 : 1;
  } else if (selectionStart < value.length) {
    to += value.codePointAt(selectionStart)! > 0xffff ? 2 : 1;
  }
  const caret = marker(value.slice(from, to) || "\u200b");
  const firstLength = value.length && value.codePointAt(0)! > 0xffff ? 2 : 1;
  const first = from === 0 ? caret : marker(value.slice(0, firstLength));
  const last = from === value.length ? caret : marker("\u200b");
  if (first !== caret) mirror.append(first, value.slice(firstLength, from));
  mirror.append(caret, value.slice(to));
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
