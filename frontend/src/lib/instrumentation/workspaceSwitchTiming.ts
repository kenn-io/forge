// Workspace-switch timing on top of the interaction timing scaffolding.
// A switch opens when the terminal view reacts to a workspace route and
// records at most one measure per phase, so the User Timing entries
// (performance.getEntriesByName("workspace-switch:<phase>")) stay stable
// for before/after comparisons across profiling runs. Runtime polling,
// SSE refreshes, and terminal reconnects re-enter the same code paths;
// the one-shot guard keeps those repeats out of the recorded switch.
//
// Only one switch is live at a time: beginning a new switch supersedes
// the previous one, and anything still holding the superseded switch
// (a stale fetch, a terminal pane from the previous workspace) records
// nothing further.

import { clearInteraction, markInteractionStart, measureInteraction } from "./interactionTiming.js";
import { beginInteractionTrace, endInteractionTrace } from "./traceContext.js";

export const WORKSPACE_SWITCH_INTERACTION = "workspace-switch";

export const WORKSPACE_SWITCH_PHASES = [
  "workspace-request-start",
  "workspace-request-end",
  "runtime-request-start",
  "runtime-request-end",
  "fonts-ready",
  "terminal-constructed",
  "socket-open",
  "first-bytes",
  "first-paint",
] as const;

export type WorkspaceSwitchPhase = (typeof WORKSPACE_SWITCH_PHASES)[number];

type PhaseDetail = Record<string, unknown>;

interface WorkspaceSwitch {
  token: string;
  workspaceId: string;
  hostKey: string | undefined;
  beganAt: number;
  recorded: Set<WorkspaceSwitchPhase>;
  traceId: string;
  traceTimeout: ReturnType<typeof setTimeout> | null;
}

// Phases arriving later than this after route selection belong to some
// later user action (a terminal launched minutes after the switch, a
// reconnect after a network blip), not to the switch itself. Dropping
// them keeps a switch's missing phases missing instead of silently
// filled with unrelated long durations.
const MAX_PHASE_AGE_MS = 30_000;

let current: WorkspaceSwitch | null = null;
let switchSeq = 0;

function endSwitchTrace(sw: WorkspaceSwitch): void {
  if (sw.traceTimeout !== null) {
    clearTimeout(sw.traceTimeout);
    sw.traceTimeout = null;
  }
  endInteractionTrace(sw.traceId);
}

// Marks route selection: the moment the terminal view reacts to a new
// workspace route. All phase measures are durations from this mark.
// The returned token identifies this switch for cancelWorkspaceSwitch.
export function beginWorkspaceSwitch(workspaceId: string, hostKey: string | undefined): string {
  if (current) {
    clearInteraction(WORKSPACE_SWITCH_INTERACTION, current.token);
    endSwitchTrace(current);
  }
  switchSeq += 1;
  const token = String(switchSeq);
  const traceId = beginInteractionTrace(WORKSPACE_SWITCH_INTERACTION, {
    "workspace.id": workspaceId,
    ...(hostKey !== undefined ? { "host.key": hostKey } : {}),
  });
  const sw: WorkspaceSwitch = {
    token,
    workspaceId,
    hostKey,
    beganAt: performance.now(),
    recorded: new Set(),
    traceId,
    traceTimeout: null,
  };
  current = sw;
  sw.traceTimeout = setTimeout(() => {
    if (current !== sw) return;
    sw.traceTimeout = null;
    endInteractionTrace(sw.traceId);
  }, MAX_PHASE_AGE_MS);
  markInteractionStart(WORKSPACE_SWITCH_INTERACTION, token);
  return token;
}

// Leaving the workspace surface ends the live switch so lingering
// panes and fetches record nothing more. With a token (from
// beginWorkspaceSwitch) only that switch is cancelled — a view being
// torn down cannot cancel a newer switch someone else began.
export function cancelWorkspaceSwitch(token?: string): void {
  if (!current) return;
  if (token !== undefined && current.token !== token) return;
  clearInteraction(WORKSPACE_SWITCH_INTERACTION, current.token);
  endSwitchTrace(current);
  current = null;
}

function recordPhase(sw: WorkspaceSwitch, phase: WorkspaceSwitchPhase, detail?: PhaseDetail): boolean {
  if (performance.now() - sw.beganAt > MAX_PHASE_AGE_MS) {
    endSwitchTrace(sw);
    return false;
  }
  if (sw.recorded.has(phase)) return false;
  sw.recorded.add(phase);
  measureInteraction(WORKSPACE_SWITCH_INTERACTION, phase, sw.token, {
    workspaceId: sw.workspaceId,
    ...(sw.hostKey !== undefined ? { hostKey: sw.hostKey } : {}),
    traceId: sw.traceId,
    ...detail,
  });
  if (phase === "first-paint") endSwitchTrace(sw);
  return true;
}

// Records a request phase for the switch, but only while the switch
// still targets the workspace the caller captured at request time — a
// slow response for a previous workspace must not measure against the
// current switch's start mark.
export function recordWorkspaceSwitchPhase(
  phase: WorkspaceSwitchPhase,
  workspaceId: string,
  hostKey: string | undefined,
  detail?: PhaseDetail,
): void {
  if (!current) return;
  if (current.workspaceId !== workspaceId || current.hostKey !== hostKey) return;
  recordPhase(current, phase, detail);
}

export interface WorkspaceSwitchPaneTimer {
  // Returns whether this call actually recorded the phase, so a pane
  // can chain dependent phases (first-paint only follows its own
  // first-bytes) instead of mixing measurements across panes.
  record(phase: WorkspaceSwitchPhase, detail?: PhaseDetail): boolean;
}

const inertPaneTimer: WorkspaceSwitchPaneTimer = {
  record() {
    return false;
  },
};

let paneSeq = 0;

// Terminal panes don't know which workspace they belong to (session
// panes only get a websocket path), so a pane binds to whatever switch
// is live when it mounts. Panes mounted before the switch — or still
// alive after a newer switch began — hold a superseded binding and
// record nothing. Phases stay one-shot across all panes of a switch
// (first pane to reach a phase wins); the paneId in each measure's
// detail says which pane that was.
export function createWorkspaceSwitchPaneTimer(): WorkspaceSwitchPaneTimer {
  const sw = current;
  if (!sw) return inertPaneTimer;
  paneSeq += 1;
  const paneId = paneSeq;
  return {
    record(phase, detail) {
      if (sw !== current) return false;
      return recordPhase(sw, phase, { paneId, ...detail });
    },
  };
}
