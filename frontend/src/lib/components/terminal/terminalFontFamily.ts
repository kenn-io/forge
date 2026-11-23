const GENERIC_FONT_FAMILIES = new Set([
  "serif",
  "sans-serif",
  "monospace",
  "cursive",
  "fantasy",
  "system-ui",
  "ui-serif",
  "ui-sans-serif",
  "ui-monospace",
  "ui-rounded",
  "emoji",
  "math",
  "fangsong",
]);

function splitFontFamilyList(value: string): string[] {
  const families: string[] = [];
  let current = "";
  let quote: '"' | "'" | null = null;
  let escaped = false;

  for (const char of value) {
    if (escaped) {
      current += char;
      escaped = false;
      continue;
    }

    if (char === "\\") {
      current += char;
      escaped = true;
      continue;
    }

    if (quote) {
      current += char;
      if (char === quote) quote = null;
      continue;
    }

    if (char === '"' || char === "'") {
      current += char;
      quote = char;
      continue;
    }

    if (char === ",") {
      const family = current.trim();
      if (family) families.push(family);
      current = "";
      continue;
    }

    current += char;
  }

  const family = current.trim();
  if (family) families.push(family);
  return families;
}

function normalizeFontFamily(family: string): string {
  const trimmed = family.trim();
  const unquoted =
    (trimmed.startsWith('"') && trimmed.endsWith('"')) || (trimmed.startsWith("'") && trimmed.endsWith("'"))
      ? trimmed.slice(1, -1)
      : trimmed;
  return unquoted.toLowerCase();
}

function isGenericFontFamily(family: string): boolean {
  return GENERIC_FONT_FAMILIES.has(normalizeFontFamily(family));
}

function appendUnique(result: string[], seen: Set<string>, family: string): void {
  const key = normalizeFontFamily(family);
  if (seen.has(key)) return;
  seen.add(key);
  result.push(family);
}

export function buildTerminalFontFamily(configuredFontFamily: string, defaultFontFamily: string): string {
  const configured = configuredFontFamily.trim();
  const fallback = defaultFontFamily.trim();

  if (!configured) return fallback || "monospace";

  const configuredFamilies = splitFontFamilyList(configured);
  const fallbackFamilies = splitFontFamilyList(fallback || "monospace");
  const result: string[] = [];
  const seen = new Set<string>();

  for (const family of configuredFamilies) {
    if (!isGenericFontFamily(family)) appendUnique(result, seen, family);
  }
  for (const family of fallbackFamilies) {
    appendUnique(result, seen, family);
  }
  for (const family of configuredFamilies) {
    if (isGenericFontFamily(family)) appendUnique(result, seen, family);
  }

  return result.join(", ");
}
