import { HARNESS_ICONS, type HarnessIconId } from "@kenn-io/kit-ui";

import type { LaunchTarget } from "../../api/types.js";

/**
 * The kit-ui harness glyph a launch target should be drawn with.
 *
 * Agent launch targets are keyed by the maintainer (built-ins such as `claude`
 * and `codex`, or config entries like `codex-review`), while kit-ui's glyphs
 * are keyed by brand (`openai` for Codex, `snowflake` for Cortex Code). Each
 * glyph therefore matches on its own id and on the agent products kit-ui lists
 * for it, both slugged the same way as the key: first segment by segment, so
 * `codex-review` resolves to the OpenAI glyph and `claude-fast` to Claude;
 * then, for names long enough to be unambiguous, as a plain string prefix of
 * the key's first segment, so a `claudex` wrapper still gets the Claude glyph
 * while `pixel` never resolves to `pi`.
 */

function segments(value: string): string[] {
  return value
    .toLowerCase()
    .split(/[^a-z0-9]+/)
    .filter((segment) => segment !== "");
}

/** Shortest name segment that may match a key as a bare string prefix. */
const LOOSE_PREFIX_MIN_LENGTH = 4;

interface Candidate {
  id: HarnessIconId;
  segments: string[];
}

const CANDIDATES: readonly Candidate[] = HARNESS_ICONS.flatMap((icon) =>
  [icon.id, ...icon.agents].map((name) => ({ id: icon.id, segments: segments(name) })),
);

/** Resolve an agent launch-target key to the glyph whose name shares its prefix. */
export function harnessForAgentKey(key: string): HarnessIconId | null {
  const keySegments = segments(key);
  const keyHead = keySegments[0];
  if (keyHead === undefined) return null;
  let best: { id: HarnessIconId; matched: number } | null = null;
  for (const candidate of CANDIDATES) {
    if (candidate.segments[0] !== keyHead) continue;
    let matched = 0;
    while (
      matched < candidate.segments.length &&
      matched < keySegments.length &&
      candidate.segments[matched] === keySegments[matched]
    ) {
      matched += 1;
    }
    if (best === null || matched > best.matched) best = { id: candidate.id, matched };
  }
  if (best !== null) return best.id;

  let loose: { id: HarnessIconId; length: number } | null = null;
  for (const candidate of CANDIDATES) {
    const head = candidate.segments[0];
    if (head === undefined || head.length < LOOSE_PREFIX_MIN_LENGTH) continue;
    if (!keyHead.startsWith(head)) continue;
    if (loose === null || head.length > loose.length) loose = { id: candidate.id, length: head.length };
  }
  return loose?.id ?? null;
}

/** The glyph for a launch target, or null for shells and unmatched agents. */
export function launchTargetHarness(target: Pick<LaunchTarget, "kind" | "key">): HarnessIconId | null {
  if (target.kind !== "agent") return null;
  return harnessForAgentKey(target.key);
}
