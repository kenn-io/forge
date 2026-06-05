import { beforeEach, describe, expect, it } from "vite-plus/test";

import {
  registerScopedActions,
  getActionsByOwner,
  getAllActions,
  registerCheatsheetEntries,
  getAllCheatsheetEntries,
  resetRegistry,
} from "./registry.svelte.js";
import type { Action, CheatsheetEntry } from "./types.js";

const trueWhen = () => true;
const noop = () => {};

const action = (id: string, scope: Action["scope"] = "global"): Action => ({
  id,
  label: id,
  scope,
  binding: null,
  priority: 0,
  when: trueWhen,
  handler: noop,
});

describe("registry", () => {
  beforeEach(() => resetRegistry());

  it("returns registered actions for an owner", () => {
    registerScopedActions("owner-a", [action("a.one"), action("a.two")]);
    expect(getActionsByOwner("owner-a")).toHaveLength(2);
  });

  it("cleanup removes only the owner's actions", () => {
    registerScopedActions("owner-a", [action("a.one")]);
    const cleanup = registerScopedActions("owner-b", [action("b.one")]);
    cleanup();
    expect(getActionsByOwner("owner-a")).toHaveLength(1);
    expect(getActionsByOwner("owner-b")).toHaveLength(0);
  });

  it("re-registering an owner replaces only its entries", () => {
    registerScopedActions("owner-a", [action("a.one")]);
    registerScopedActions("owner-b", [action("b.one")]);
    registerScopedActions("owner-a", [action("a.two")]);
    expect(getActionsByOwner("owner-a").map((a) => a.id)).toEqual(["a.two"]);
    expect(getActionsByOwner("owner-b").map((a) => a.id)).toEqual(["b.one"]);
  });

  it("getAllActions flattens entries across owners", () => {
    registerScopedActions("owner-a", [action("a.one")]);
    registerScopedActions("owner-b", [action("b.one")]);
    expect(
      getAllActions()
        .map((a) => a.id)
        .sort(),
    ).toEqual(["a.one", "b.one"]);
  });

  it("registerCheatsheetEntries supports owner-based replacement", () => {
    const entry = (id: string): CheatsheetEntry => ({
      id,
      label: id,
      binding: { key: id },
      scope: "view-pulls",
    });
    registerCheatsheetEntries("ce-a", [entry("a")]);
    registerCheatsheetEntries("ce-b", [entry("b")]);
    expect(
      getAllCheatsheetEntries()
        .map((e) => e.id)
        .sort(),
    ).toEqual(["a", "b"]);
    registerCheatsheetEntries("ce-a", []);
    expect(getAllCheatsheetEntries().map((e) => e.id)).toEqual(["b"]);
  });
});

describe("conflict assertion", () => {
  beforeEach(() => resetRegistry());

  it("throws when two actions share (binding, scope, priority)", () => {
    const collide: Action[] = [{ ...action("a.one"), binding: { key: "k", ctrlOrMeta: true } }];
    const collide2: Action[] = [{ ...action("a.two"), binding: { key: "k", ctrlOrMeta: true } }];
    registerScopedActions("o1", collide);
    expect(() => registerScopedActions("o2", collide2)).toThrow(/conflict/i);
  });

  it("allows different scopes with the same binding", () => {
    registerScopedActions("o1", [{ ...action("a", "view-pulls"), binding: { key: "j" } }]);
    registerScopedActions("o2", [{ ...action("b", "view-issues"), binding: { key: "j" } }]);
    expect(getAllActions()).toHaveLength(2);
  });
});
