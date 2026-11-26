// Converts a markdown PR or issue body into a compact plain-text excerpt
// suitable for a hover preview card. Strips comments, fenced code blocks,
// images, HTML tags, and markdown formatting while keeping readable prose.
export function summarizeMarkdownBody(raw: string | null | undefined, maxLength = 220): string {
  if (!raw) return "";

  let text = raw
    // Drop HTML comments (often PR template instructions).
    .replace(/<!--[\s\S]*?-->/g, " ")
    // Drop fenced code blocks so stack traces or diffs do not flood the card.
    .replace(/(^|\n)[ \t]*(```+|~~~+)[^\n]*\n[\s\S]*?\n[ \t]*\2[ \t]*(?=\n|$)/g, "\n")
    // Drop standalone markdown images (screenshots, badges).
    .replace(/!\[[^\]]*\]\([^)]*\)/g, " ")
    .replace(/!\[[^\]]*\]\[[^\]]*\]/g, " ")
    // Unwrap markdown links: [label](url) -> label.
    .replace(/\[([^\]]+)\]\([^)]*\)/g, "$1")
    .replace(/\[([^\]]+)\]\[[^\]]*\]/g, "$1")
    // Drop link reference definitions: [id]: https://...
    .replace(/^[ \t]*\[[^\]]+\]:[ \t]*\S+[^\n]*$/gm, " ")
    // Unwrap angle-bracket autolinks: <https://...> -> https://...
    .replace(/<(https?:\/\/[^>\s]+)>/gi, "$1")
    // Strip remaining HTML tags.
    .replace(/<\/?[a-z][^>]*>/gi, " ")
    // Strip block-level prefixes (headings, blockquotes, horizontal rules, list markers, task checkboxes).
    .replace(/^[ \t]*(?:-{3,}|\*{3,}|_{3,})[ \t]*$/gm, " ")
    .replace(/^[ \t]*#{1,6}[ \t]+/gm, "")
    .replace(/^[ \t]*(?:>[ \t]*)+/gm, "")
    .replace(/^[ \t]*(?:[-*+]|\d+\.)[ \t]+(?:\[[ xX]\][ \t]+)?/gm, "")
    // Strip inline code backticks while preserving the identifier.
    .replace(/`([^`]+)`/g, "$1")
    // Strip bold/strikethrough/italic markers while preserving underscores inside words.
    .replace(/\*\*([^*]+)\*\*/g, "$1")
    .replace(/__([^_]+)__/g, "$1")
    .replace(/~~([^~]+)~~/g, "$1")
    .replace(/(^|[^\w])\*([^*\n]+)\*(?!\w)/g, "$1$2")
    .replace(/(^|[^\w])_([^_\n]+)_(?!\w)/g, "$1$2")
    // Collapse whitespace and line breaks into single spaces.
    .replace(/\s+/g, " ")
    .trim();

  if (!text) return "";
  if (text.length <= maxLength) return text;

  const sliced = text.slice(0, maxLength);
  const lastSpace = sliced.lastIndexOf(" ");
  const cut = lastSpace >= Math.floor(maxLength * 0.6) ? sliced.slice(0, lastSpace) : sliced;
  return `${cut.replace(/[.,;:!?-]+$/, "").trimEnd()}…`;
}
