import { describe, expect, it } from "vite-plus/test";

import type { Label } from "../../api/types.js";
import { nextCatalogLabelNames } from "./labelSelection.js";

const bug: Label = { name: "bug", color: "d73a4a" };
const triage: Label = { name: "triage", color: "fbca04" };
const stale: Label = { name: "legacy", color: "999999" };

describe("nextCatalogLabelNames", () => {
  it("drops assigned labels missing from the catalog when adding a label", () => {
    expect(nextCatalogLabelNames([bug, stale], [bug, triage], "triage")).toEqual(["bug", "triage"]);
  });

  it("drops assigned labels missing from the catalog when removing a label", () => {
    expect(nextCatalogLabelNames([bug, stale], [bug, triage], "bug")).toEqual([]);
  });
});
