export type DetailActivityViewMode = "normal" | "compact";
export type DetailTimelineOrder = "grouped" | "chronological";

const STORAGE_KEY = "kenn-forge-detail-activity-view";
const ORDER_STORAGE_KEY = "kenn-forge-detail-timeline-order";

function readFromStorage(): DetailActivityViewMode {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    return raw === "compact" ? "compact" : "normal";
  } catch {
    return "normal";
  }
}

function readOrderFromStorage(): DetailTimelineOrder {
  try {
    const raw = localStorage.getItem(ORDER_STORAGE_KEY);
    return raw === "chronological" ? "chronological" : "grouped";
  } catch {
    return "grouped";
  }
}

export function createDetailActivityViewStore() {
  let mode = $state<DetailActivityViewMode>(readFromStorage());
  let order = $state<DetailTimelineOrder>(readOrderFromStorage());

  function getMode(): DetailActivityViewMode {
    return mode;
  }

  function setMode(value: DetailActivityViewMode): void {
    mode = value;
    try {
      localStorage.setItem(STORAGE_KEY, value);
    } catch {
      // localStorage unavailable.
    }
  }

  function getOrder(): DetailTimelineOrder {
    return order;
  }

  function setOrder(value: DetailTimelineOrder): void {
    order = value;
    try {
      localStorage.setItem(ORDER_STORAGE_KEY, value);
    } catch {
      // localStorage unavailable.
    }
  }

  return {
    getMode,
    setMode,
    getOrder,
    setOrder,
  };
}

export type DetailActivityViewStore = ReturnType<typeof createDetailActivityViewStore>;
