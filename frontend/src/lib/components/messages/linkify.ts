export interface Segment {
  kind: "text" | "url";
  value: string;
  href: string;
}

const URL_RE = /https?:\/\/[^\s<>"']+|www\.[^\s<>"']+/g;

function stripTrailingPunctuation(url: string): { url: string; punctuation: string } {
  let trimmed = url;
  let punctuation = "";

  while (trimmed.length > 0) {
    const ch = trimmed[trimmed.length - 1];
    if (".,;:!?]>".includes(ch ?? "")) {
      punctuation = ch + punctuation;
      trimmed = trimmed.slice(0, -1);
      continue;
    }

    if (ch === ")") {
      const opens = (trimmed.match(/\(/g) ?? []).length;
      const closes = (trimmed.match(/\)/g) ?? []).length;
      if (closes > opens) {
        punctuation = ch + punctuation;
        trimmed = trimmed.slice(0, -1);
        continue;
      }
    }

    break;
  }

  return { url: trimmed, punctuation };
}

export function linkify(text: string): Segment[] {
  const segments: Segment[] = [];
  let cursor = 0;

  for (const match of text.matchAll(URL_RE)) {
    const matchStart = match.index;
    if (matchStart === undefined) continue;

    const rawMatch = match[0] ?? "";
    if (matchStart > cursor) {
      segments.push({ kind: "text", value: text.slice(cursor, matchStart), href: "" });
    }

    const { url, punctuation } = stripTrailingPunctuation(rawMatch);
    const href = url.startsWith("www.") ? `https://${url}` : url;
    segments.push({ kind: "url", value: url, href });
    cursor = matchStart + rawMatch.length;

    if (punctuation) {
      segments.push({ kind: "text", value: punctuation, href: "" });
    }
  }

  if (cursor < text.length) {
    segments.push({ kind: "text", value: text.slice(cursor), href: "" });
  }

  return segments;
}
