// Interaction timing scaffolding on the standard User Timing API. Measures
// land in the DevTools Performance panel (Timings track) and are queryable
// anywhere via performance.getEntriesByName("<interaction>:<phase>"), so
// slow interactions can be quantified in the browser, Playwright, or tests
// without bespoke logging.
//
// An interaction is opened with a start mark keyed by a caller-chosen token
// (so overlapping instances of the same interaction stay separate), then any
// number of phases are measured against that mark. Clearing the interaction
// drops the mark so superseded or failed instances record nothing further.

type InteractionDetail = Record<string, unknown>;

function startMarkName(interaction: string, token: string): string {
  return `${interaction}:start:${token}`;
}

function perf(): Performance | null {
  return typeof performance === "undefined" ? null : performance;
}

export function markInteractionStart(interaction: string, token: string): void {
  const p = perf();
  if (typeof p?.mark !== "function") return;
  try {
    p.mark(startMarkName(interaction, token));
  } catch {
    // Timing is diagnostics only; never let it break the interaction itself.
  }
}

export function measureInteraction(
  interaction: string,
  phase: string,
  token: string,
  detail?: InteractionDetail,
): void {
  const p = perf();
  if (typeof p?.measure !== "function" || typeof p.getEntriesByName !== "function") return;
  const start = startMarkName(interaction, token);
  if (p.getEntriesByName(start, "mark").length === 0) return;
  const name = `${interaction}:${phase}`;
  try {
    p.measure(name, { start, detail });
  } catch {
    // Older User Timing implementations only accept positional arguments.
    try {
      p.measure(name, start);
    } catch {
      // Timing is diagnostics only; never let it break the interaction itself.
    }
  }
}

export function clearInteraction(interaction: string, token: string): void {
  const p = perf();
  if (typeof p?.clearMarks !== "function") return;
  p.clearMarks(startMarkName(interaction, token));
}
