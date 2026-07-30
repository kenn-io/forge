import { beforeEach, describe, expect, it } from "vitest";
import { migrateKennForgeStorage } from "./kennForgeStorageMigration";

describe("migrateKennForgeStorage", () => {
  beforeEach(() => localStorage.clear());

  it("moves static and dynamic legacy keys", () => {
    const legacy = "middle" + "man";
    localStorage.setItem(`${legacy}:theme`, "dark");
    localStorage.setItem(`repo:${legacy}:acme/widget`, "open");

    migrateKennForgeStorage(localStorage);

    expect(localStorage.getItem("kenn-forge:theme")).toBe("dark");
    expect(localStorage.getItem("repo:kenn-forge:acme/widget")).toBe("open");
    expect(localStorage.getItem(`${legacy}:theme`)).toBeNull();
  });

  it("preserves an existing canonical value", () => {
    const legacy = "middle" + "man";
    localStorage.setItem(`${legacy}:theme`, "light");
    localStorage.setItem("kenn-forge:theme", "dark");

    migrateKennForgeStorage(localStorage);

    expect(localStorage.getItem("kenn-forge:theme")).toBe("dark");
    expect(localStorage.getItem(`${legacy}:theme`)).toBeNull();
  });
});
