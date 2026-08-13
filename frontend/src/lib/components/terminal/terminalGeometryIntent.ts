const terminalGeometryIntentWindowMs = 250;

let geometryIntentActive = false;
let geometryIntentGeneration = 0;
let geometryIntentTimer: ReturnType<typeof setTimeout> | undefined;

function keepTerminalGeometryIntentAlive(): void {
  geometryIntentActive = true;
  if (geometryIntentTimer !== undefined) {
    clearTimeout(geometryIntentTimer);
  }
  geometryIntentTimer = setTimeout(() => {
    geometryIntentActive = false;
    geometryIntentTimer = undefined;
  }, terminalGeometryIntentWindowMs);
}

export function beginTerminalGeometryIntent(): number {
  geometryIntentGeneration += 1;
  keepTerminalGeometryIntentAlive();
  return geometryIntentGeneration;
}

export function extendTerminalGeometryIntent(): void {
  keepTerminalGeometryIntentAlive();
}

export function currentTerminalGeometryIntent(): number | null {
  return geometryIntentActive ? geometryIntentGeneration : null;
}

export function hasTerminalGeometryIntent(): boolean {
  return currentTerminalGeometryIntent() !== null;
}
