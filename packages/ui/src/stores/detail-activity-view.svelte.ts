export type DetailActivityViewMode = "normal" | "compact";

const STORAGE_KEY = "middleman-detail-activity-view";

function readFromStorage(): DetailActivityViewMode {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    return raw === "compact" ? "compact" : "normal";
  } catch {
    return "normal";
  }
}

export function createDetailActivityViewStore() {
  let mode = $state<DetailActivityViewMode>(readFromStorage());

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

  return {
    getMode,
    setMode,
  };
}

export type DetailActivityViewStore = ReturnType<typeof createDetailActivityViewStore>;
