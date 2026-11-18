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
  // The caret at either end of the text is on that end's line.
  if (selectionStart === (edge === "first" ? 0 : value.length)) return true;
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
  // Each marker wraps a character of the text, since Firefox places an empty
  // span at a different height and an added character can wrap onto a line
  // of its own. At a soft wrap one caret offset is drawn either at the end of
  // the upper line (after End) or the start of the lower one (after Home),
  // and the field does not say which. So the caret counts as on an edge line
  // only if both places are: the first line takes the character after the
  // caret, and the last line the one before it, unless that is a newline.
  // Otherwise the arrow moves the caret as usual.
  const marker = (text: string) => Object.assign(document.createElement("span"), { textContent: text });
  const codePointBefore = (index: number) =>
    index > 1 && /[\uDC00-\uDFFF]/.test(value[index - 1]!) ? index - 2 : index - 1;
  let from = selectionStart;
  let to = selectionStart;
  if ((edge === "last" || to === value.length) && from > 0 && value[from - 1] !== "\n") from = codePointBefore(from);
  else if (to < value.length) to += value.codePointAt(from)! > 0xffff ? 2 : 1;
  // At the end of the text, right after a newline: a line comes before it.
  else return false;
  const caret = marker(value.slice(from, to));
  let edgeMarker: HTMLElement;
  if (edge === "first") {
    const firstEnd = value.codePointAt(0)! > 0xffff ? 2 : 1;
    edgeMarker = from < firstEnd ? caret : marker(value.slice(0, firstEnd));
    mirror.append(...(edgeMarker === caret ? [] : [edgeMarker, value.slice(firstEnd, from)]), caret, value.slice(to));
  } else if (value.endsWith("\n")) {
    // A trailing newline opens a line only once something follows it.
    edgeMarker = marker("\u200b");
    mirror.append(value.slice(0, from), caret, value.slice(to), edgeMarker);
  } else {
    const lastStart = codePointBefore(value.length);
    edgeMarker = to > lastStart ? caret : marker(value.slice(lastStart));
    mirror.append(
      value.slice(0, from),
      caret,
      ...(edgeMarker === caret ? [value.slice(to)] : [value.slice(to, lastStart), edgeMarker]),
    );
  }
  document.body.append(mirror);
  try {
    const top = caret.getBoundingClientRect().top;
    const edgeTop = edgeMarker.getBoundingClientRect().top;
    return Math.abs(top - edgeTop) < 1;
  } finally {
    mirror.remove();
  }
}
