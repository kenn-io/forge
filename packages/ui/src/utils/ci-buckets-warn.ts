import type { CICheck } from "../api/types.js";

// Hard cap on how much of a raw `conclusion` value we keep in memory or
// log. A misbehaving provider could in theory send a multi-KB conclusion
// string; capping at 128 chars bounds memory growth (Set entry size) and
// log line length without losing the diagnostic signal — real conclusions
// are short identifiers ("success", "failure", "timed_out", etc.).
const UNKNOWN_CONCLUSION_DISPLAY_MAX = 128;

const warnedUnknown = new Set<string>();
const warnedMalformed = new Set<string>();

function truncateConclusion(c: string): string {
  return c.length > UNKNOWN_CONCLUSION_DISPLAY_MAX ? `${c.slice(0, UNKNOWN_CONCLUSION_DISPLAY_MAX)}…` : c;
}

export function warnOnUnknownConclusions(unknown: CICheck[], context?: { repo?: string; number?: number }): void {
  const id = context?.repo && context?.number !== undefined ? `${context.repo}#${context.number}` : "";
  const idPrefix = id ? `[${id}] ` : "";
  for (const c of unknown) {
    const display = truncateConclusion(c.conclusion);
    if (warnedUnknown.has(display)) continue;
    warnedUnknown.add(display);
    console.warn(`${idPrefix}Unrecognised CI conclusion: ${display}`);
  }
}

/** @internal test helper */
export function __resetCIWarnings(): void {
  warnedUnknown.clear();
  warnedMalformed.clear();
}

const DEV_PAYLOAD_PREVIEW_MAX = 64;

// Read DEV at call time, not module load time, so vi.stubEnv("DEV", ...)
// in tests can toggle production vs dev behaviour without re-importing
// the module. Production builds inline this away via Vite's compile-time
// `import.meta.env.DEV` replacement.
function isDevMode(): boolean {
  return typeof import.meta !== "undefined" && import.meta.env.DEV === true;
}

export function warnOnMalformedCIChecksJSON(
  raw: string,
  error: Error,
  context?: { repo?: string; number?: number },
): void {
  const id = context?.repo && context?.number !== undefined ? `${context.repo}#${context.number}` : "";
  // Dedupe by raw payload bytes. Bounded by the number of distinct
  // malformed payloads ever seen, which is one or two in practice.
  const key = `${id}|${raw}`;
  if (warnedMalformed.has(key)) return;
  warnedMalformed.add(key);
  const idPrefix = id ? `[${id}] ` : "";
  // Production: log only metadata (length + a stable "Malformed JSON"
  // label, or the locally-created shape error prefix). Never forward
  // `error.message` -- native JSON.parse messages typically include a
  // preview of the malformed input.
  // Dev: also log error.message and a 64-char input preview. Dev builds
  // are not the privacy boundary.
  const safeLabel = error.message.startsWith("CIChecksJSON: ") ? error.message : "Malformed JSON";
  if (isDevMode()) {
    const previewClause = `\nPreview: ${raw.slice(0, DEV_PAYLOAD_PREVIEW_MAX)}${raw.length > DEV_PAYLOAD_PREVIEW_MAX ? "…" : ""}`;
    console.warn(`${idPrefix}Malformed CIChecksJSON: ${error.message} (length=${raw.length})${previewClause}`);
  } else {
    console.warn(`${idPrefix}Malformed CIChecksJSON: ${safeLabel} (length=${raw.length})`);
  }
}
