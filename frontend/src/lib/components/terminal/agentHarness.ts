import { HARNESSES, harnessInfo, type HarnessId } from "@kenn-io/kit-ui";

import type { LaunchTarget } from "../../api/types.js";

/**
 * The kit-ui harness wordmark a launch target should be drawn with.
 *
 * Agent launch targets are keyed by the maintainer (built-ins such as `claude`
 * and `codex`, or config entries like `codex-review`), while kit-ui's harness
 * ids follow the agentsview naming (`claude-code`). The two are matched on a
 * shared leading prefix, segment by segment, so `claude`, `claude-code` and
 * `claude-fast` all resolve to the Claude Code mark and `codex-review` to
 * Codex, but `pixel` never resolves to `pi`.
 */
export interface LaunchTargetMark {
  harness: HarnessId;
  /**
   * Whether the target's own label still adds information beside the
   * wordmark. A built-in `Claude` under the `Claude Code` mark does not; a
   * config `Review Agent` under the Codex mark does.
   */
  showLabel: boolean;
}

function segments(value: string): string[] {
  return value
    .toLowerCase()
    .split(/[^a-z0-9]+/)
    .filter((segment) => segment !== "");
}

/** Resolve an agent launch-target key to the harness whose id shares its prefix. */
export function harnessForAgentKey(key: string): HarnessId | null {
  const keySegments = segments(key);
  if (keySegments.length === 0) return null;
  let best: { id: HarnessId; matched: number } | null = null;
  for (const harness of HARNESSES) {
    const idSegments = segments(harness.id);
    if (idSegments[0] !== keySegments[0]) continue;
    let matched = 0;
    while (
      matched < idSegments.length &&
      matched < keySegments.length &&
      idSegments[matched] === keySegments[matched]
    ) {
      matched += 1;
    }
    if (best === null || matched > best.matched) best = { id: harness.id, matched };
  }
  return best?.id ?? null;
}

export function launchTargetMark(target: Pick<LaunchTarget, "kind" | "key" | "label">): LaunchTargetMark | null {
  if (target.kind !== "agent") return null;
  const harness = harnessForAgentKey(target.key);
  if (harness === null) return null;
  const label = target.label.trim().toLowerCase();
  const harnessLabel = harnessInfo(harness).label.toLowerCase();
  return { harness, showLabel: label !== "" && !harnessLabel.startsWith(label) };
}
