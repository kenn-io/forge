function createMemoryStorage(): Storage {
  const data = new Map<string, string>();

  return {
    get length() {
      return data.size;
    },
    clear() {
      data.clear();
    },
    getItem(key: string) {
      return data.get(key) ?? null;
    },
    key(index: number) {
      return Array.from(data.keys())[index] ?? null;
    },
    removeItem(key: string) {
      data.delete(key);
    },
    setItem(key: string, value: string) {
      data.set(key, value);
    },
  };
}

const storage = createMemoryStorage();

if (typeof globalThis.localStorage === "undefined" || typeof globalThis.localStorage.getItem !== "function") {
  Object.defineProperty(globalThis, "localStorage", {
    configurable: true,
    writable: true,
    value: storage,
  });
}

if (typeof document !== "undefined" && typeof document.elementFromPoint !== "function") {
  Object.defineProperty(document, "elementFromPoint", {
    configurable: true,
    value: () => null,
  });
}

// jsdom has no layout, so Element.scrollIntoView does not exist. kit-ui's
// SelectDropdown scrolls the selected option into view when the menu opens;
// give it a no-op so dropdown tests can open menus.
if (typeof Element !== "undefined" && typeof Element.prototype.scrollIntoView !== "function") {
  Object.defineProperty(Element.prototype, "scrollIntoView", {
    configurable: true,
    writable: true,
    value: () => {},
  });
}

// jsdom also lacks ResizeObserver, which kit-ui's popover auto-reposition
// (SelectDropdown, Tooltip, FilterDropdown) instantiates when a panel opens.
if (typeof globalThis.ResizeObserver === "undefined") {
  class ResizeObserverStub {
    observe(): void {}
    unobserve(): void {}
    disconnect(): void {}
  }
  Object.defineProperty(globalThis, "ResizeObserver", {
    configurable: true,
    writable: true,
    value: ResizeObserverStub,
  });
}
