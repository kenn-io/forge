import { afterEach, describe, expect, it } from "vite-plus/test";

import { cycleTriFilter, readTriFilter, triFilterRow, writeTriFilter } from "./tri-filter.js";

describe("tri-state filters", () => {
  const key = "kenn-forge:test:tri-filter";
  afterEach(() => localStorage.removeItem(key));

  it("cycles off, required, excluded, and back to off", () => {
    expect(cycleTriFilter(undefined)).toBe("require");
    expect(cycleTriFilter("require")).toBe("exclude");
    expect(cycleTriFilter("exclude")).toBeUndefined();
  });

  it("ticks a required row and crosses an excluded one", () => {
    expect(triFilterRow(undefined)).toEqual({ active: false, excluded: false });
    expect(triFilterRow("require")).toEqual({ active: true, excluded: false });
    expect(triFilterRow("exclude")).toEqual({ active: false, excluded: true });
  });

  it("keeps a filter a browser saved before filters could be excluded", () => {
    localStorage.setItem(key, "1");
    expect(readTriFilter(key)).toBe("require");
  });

  it("remembers each state across a reload", () => {
    writeTriFilter(key, "exclude");
    expect(readTriFilter(key)).toBe("exclude");
    writeTriFilter(key, "require");
    expect(readTriFilter(key)).toBe("require");
    writeTriFilter(key, undefined);
    expect(localStorage.getItem(key)).toBeNull();
    expect(readTriFilter(key)).toBeUndefined();
  });
});
