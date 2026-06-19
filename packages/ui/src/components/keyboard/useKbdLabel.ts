import type { KeySpec } from "../../stores/keyboard/keyspec.js";

function isMacPlatform(): boolean {
  if (typeof navigator === "undefined") return false;
  const platform = navigator.platform ?? "";
  const userAgent = navigator.userAgent ?? "";
  return /Mac|iPhone|iPad|iPod/.test(platform || userAgent);
}

export function kbdGlyph(spec: KeySpec): string {
  return kbdGlyphParts(spec).join(kbdGlyphJoiner());
}

function kbdGlyphParts(spec: KeySpec): string[] {
  const isMac = isMacPlatform();
  const parts: string[] = [];
  if (spec.ctrlOrMeta) parts.push(isMac ? "⌘" : "Ctrl");
  if (spec.shift) parts.push(isMac ? "⇧" : "Shift");
  if (spec.alt) parts.push(isMac ? "⌥" : "Alt");
  if (spec.key === " ") parts.push("Space");
  else parts.push(spec.key.length === 1 ? spec.key.toUpperCase() : spec.key);
  return parts;
}

export function kbdGlyphJoiner(): "" | "+" {
  return isMacPlatform() ? "" : "+";
}

export function kbdAriaLabel(spec: KeySpec): string {
  const isMac = isMacPlatform();
  const parts: string[] = [];
  if (spec.ctrlOrMeta) parts.push(isMac ? "Command" : "Control");
  if (spec.shift) parts.push("Shift");
  if (spec.alt) parts.push(isMac ? "Option" : "Alt");
  parts.push(spec.key === " " ? "Space" : spec.key);
  return parts.join("-");
}
