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
  recorded: Set<WorkspaceSwitchPhase>;
}

let current: WorkspaceSwitch | null = null;
let switchSeq = 0;

// Marks route selection: the moment the terminal view reacts to a new
// workspace route. All phase measures are durations from this mark.
export function beginWorkspaceSwitch(workspaceId: string, hostKey: string | undefined): void {
  if (current) {
    clearInteraction(WORKSPACE_SWITCH_INTERACTION, current.token);
  }
  switchSeq += 1;
  const token = String(switchSeq);
  current = { token, workspaceId, hostKey, recorded: new Set() };
  markInteractionStart(WORKSPACE_SWITCH_INTERACTION, token);
}

// Leaving the workspace surface (e.g. the bare /workspaces route) ends
// the live switch so lingering panes and fetches record nothing more.
export function cancelWorkspaceSwitch(): void {
  if (!current) return;
  clearInteraction(WORKSPACE_SWITCH_INTERACTION, current.token);
  current = null;
}

function recordPhase(sw: WorkspaceSwitch, phase: WorkspaceSwitchPhase, detail?: PhaseDetail): void {
  if (sw.recorded.has(phase)) return;
  sw.recorded.add(phase);
  measureInteraction(WORKSPACE_SWITCH_INTERACTION, phase, sw.token, {
    workspaceId: sw.workspaceId,
    ...(sw.hostKey !== undefined ? { hostKey: sw.hostKey } : {}),
    ...detail,
  });
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
  record(phase: WorkspaceSwitchPhase, detail?: PhaseDetail): void;
}

const inertPaneTimer: WorkspaceSwitchPaneTimer = {
  record() {},
};

// Terminal panes don't know which workspace they belong to (session
// panes only get a websocket path), so a pane binds to whatever switch
// is live when it mounts. Panes mounted before the switch — or still
// alive after a newer switch began — hold a superseded binding and
// record nothing.
export function createWorkspaceSwitchPaneTimer(): WorkspaceSwitchPaneTimer {
  const sw = current;
  if (!sw) return inertPaneTimer;
  return {
    record(phase, detail) {
      if (sw !== current) return;
      recordPhase(sw, phase, detail);
    },
  };
}
