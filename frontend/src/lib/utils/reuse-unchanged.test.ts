import { describe, expect, it } from "vite-plus/test";
import { reuseUnchanged } from "./reuse-unchanged.js";

interface Row {
  ID: number;
  Title: string;
  labels: { name: string }[];
}

const keyOf = (row: Row) => row.ID;

describe("reuseUnchanged", () => {
  it("returns the previous array when a refetch changed nothing", () => {
    const prev: Row[] = [
      { ID: 1, Title: "a", labels: [{ name: "bug" }] },
      { ID: 2, Title: "b", labels: [] },
    ];
    const next = structuredClone(prev);

    expect(reuseUnchanged(prev, next, keyOf)).toBe(prev);
  });

  it("keeps unchanged rows and takes changed, new, and reordered rows from the refetch", () => {
    const unchanged = { ID: 1, Title: "a", labels: [{ name: "bug" }] };
    const changed = { ID: 2, Title: "b", labels: [] };
    const prev: Row[] = [unchanged, changed];
    const next: Row[] = [
      { ID: 3, Title: "new", labels: [] },
      { ID: 2, Title: "b", labels: [{ name: "docs" }] },
      structuredClone(unchanged),
    ];

    const merged = reuseUnchanged(prev, next, keyOf);

    expect(merged).toEqual(next);
    expect(merged[0]).toBe(next[0]);
    expect(merged[1]).toBe(next[1]);
    expect(merged[2]).toBe(unchanged);
  });

  it("returns a new array when rows were removed", () => {
    const prev: Row[] = [
      { ID: 1, Title: "a", labels: [] },
      { ID: 2, Title: "b", labels: [] },
    ];

    const merged = reuseUnchanged(prev, [structuredClone(prev[0]!)], keyOf);

    expect(merged).toEqual([prev[0]]);
    expect(merged[0]).toBe(prev[0]);
  });
});
