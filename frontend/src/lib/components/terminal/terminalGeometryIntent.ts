const terminalGeometryIntentWindowMs = 250;

let geometryIntentActive = false;
let geometryIntentTimer: ReturnType<typeof setTimeout> | undefined;

export function markTerminalGeometryIntent(): void {
  geometryIntentActive = true;
  if (geometryIntentTimer !== undefined) {
    clearTimeout(geometryIntentTimer);
  }
  geometryIntentTimer = setTimeout(() => {
    geometryIntentActive = false;
    geometryIntentTimer = undefined;
  }, terminalGeometryIntentWindowMs);
}

export function hasTerminalGeometryIntent(): boolean {
  return geometryIntentActive;
}
