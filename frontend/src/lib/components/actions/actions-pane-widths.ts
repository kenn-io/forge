export interface ActionsPaneWidths {
  rail: number;
  catalog: number;
  dispatch: number;
}

export type ActionsPane = keyof ActionsPaneWidths;

interface PaneBounds {
  min: number;
  max: number;
  initial: number;
}

// Dispatch is a short form and the runs table is the scan surface, so the
// resizable panes start narrow and the runs pane takes everything left over.
export const actionsPaneBounds: Record<ActionsPane, PaneBounds> = {
  rail: { min: 150, max: 360, initial: 200 },
  catalog: { min: 170, max: 420, initial: 230 },
  dispatch: { min: 240, max: 640, initial: 300 },
};

export const minRunsPaneWidth = 560;
export const resizeHandleWidth = 4;

const storageKey = "kenn-forge-actions-pane-widths";
// Under pressure the panes nearest the runs table give way first.
const shrinkOrder: readonly ActionsPane[] = ["dispatch", "catalog", "rail"];

export function clampPaneWidth(pane: ActionsPane, width: number): number {
  const { min, max } = actionsPaneBounds[pane];
  return Math.max(min, Math.min(max, Math.round(width)));
}

export function defaultPaneWidths(): ActionsPaneWidths {
  return {
    rail: actionsPaneBounds.rail.initial,
    catalog: actionsPaneBounds.catalog.initial,
    dispatch: actionsPaneBounds.dispatch.initial,
  };
}

// Shrinks the requested widths so the runs pane keeps its minimum inside
// layoutWidth. A zero layoutWidth means the layout is not measured yet.
export function fitPaneWidths(
  requested: ActionsPaneWidths,
  layoutWidth: number,
  visible: readonly ActionsPane[],
): ActionsPaneWidths {
  const fitted = { ...requested };
  if (layoutWidth <= 0) return fitted;
  const used = visible.reduce((sum, pane) => sum + fitted[pane] + resizeHandleWidth, 0);
  let overflow = used + minRunsPaneWidth - layoutWidth;
  for (const pane of shrinkOrder) {
    if (overflow <= 0) break;
    if (!visible.includes(pane)) continue;
    const take = Math.min(overflow, fitted[pane] - actionsPaneBounds[pane].min);
    fitted[pane] -= take;
    overflow -= take;
  }
  return fitted;
}

export function loadPaneWidths(): ActionsPaneWidths {
  const widths = defaultPaneWidths();
  try {
    const stored: unknown = JSON.parse(localStorage.getItem(storageKey) ?? "null");
    if (stored && typeof stored === "object") {
      for (const pane of shrinkOrder) {
        const value = (stored as Record<string, unknown>)[pane];
        if (typeof value === "number" && Number.isFinite(value)) widths[pane] = clampPaneWidth(pane, value);
      }
    }
  } catch {
    // Unreadable or blocked storage falls back to the defaults.
  }
  return widths;
}

export function savePaneWidths(widths: ActionsPaneWidths): void {
  try {
    localStorage.setItem(storageKey, JSON.stringify(widths));
  } catch {
    // Persistence is best effort.
  }
}
