import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import { dispatchKeydown } from "./dispatch.svelte.js";
import { defaultActions } from "./actions.js";
import { registerScopedActions, resetRegistry } from "./registry.svelte.js";
import { isSidebarCollapsed, setSidebarCollapsed } from "../sidebar.svelte.js";
import { pushModalFrame, resetModalStack } from "@middleman/ui/stores/keyboard/modal-stack";
import type { Action, Context } from "./types.js";

const flashModule = await import("@middleman/ui/stores/flash");

const ctx: Context = {
  page: "pulls",
  route: { page: "pulls" } as never,
  selectedPR: null,
  selectedIssue: null,
  isDiffView: false,
  detailTab: "conversation",
  sidebarTargetAvailable: true,
};

const event = (init: Partial<KeyboardEvent>) =>
  Object.assign(new KeyboardEvent("keydown", init), {
    preventDefault: vi.fn(),
  });

afterEach(() => {
  setSidebarCollapsed(false);
});

describe("dispatchKeydown — global registry", () => {
  beforeEach(() => {
    resetRegistry();
    resetModalStack();
  });

  it("runs the matching action's handler and preventDefaults", () => {
    const handler = vi.fn();
    const a: Action = {
      id: "go.next",
      label: "Next",
      scope: "view-pulls",
      binding: { key: "j" },
      priority: 0,
      when: () => true,
      handler,
    };
    registerScopedActions("test", [a]);
    const e = event({ key: "j" });
    dispatchKeydown(e, () => ctx);
    expect(handler).toHaveBeenCalled();
    expect(e.preventDefault).toHaveBeenCalled();
  });

  it("does not run actions whose when returns false", () => {
    const handler = vi.fn();
    const a: Action = {
      id: "go.next",
      label: "Next",
      scope: "view-pulls",
      binding: { key: "j" },
      priority: 0,
      when: () => false,
      handler,
    };
    registerScopedActions("test", [a]);
    dispatchKeydown(event({ key: "j" }), () => ctx);
    expect(handler).not.toHaveBeenCalled();
  });

  it("reserves Cmd+[ on pages where the sidebar command is hidden", () => {
    registerScopedActions("defaults", defaultActions);
    setSidebarCollapsed(false);
    const e = event({ key: "[", metaKey: true });

    dispatchKeydown(e, () => ({
      ...ctx,
      page: "activity",
      route: { page: "activity" } as never,
    }));

    expect(e.preventDefault).toHaveBeenCalled();
    expect(isSidebarCollapsed()).toBe(false);
  });
});

describe("dispatchKeydown — modal stack", () => {
  beforeEach(() => {
    resetRegistry();
    resetModalStack();
  });

  it("blocks global handlers when modal stack is non-empty", () => {
    const globalHandler = vi.fn();
    registerScopedActions("g", [
      {
        id: "g.next",
        label: "x",
        scope: "view-pulls",
        binding: { key: "j" },
        priority: 0,
        when: () => true,
        handler: globalHandler,
      },
    ]);
    pushModalFrame("modal", []);
    dispatchKeydown(event({ key: "j" }), () => ctx);
    expect(globalHandler).not.toHaveBeenCalled();
  });

  it("preventDefaults reserved palette keys when no frame action matches", () => {
    for (const init of [
      { key: "k", metaKey: true },
      { key: "p", metaKey: true },
      { key: "p", metaKey: true, shiftKey: true },
    ]) {
      pushModalFrame("modal", []);
      const e = event(init);
      dispatchKeydown(e, () => ctx);
      expect(e.preventDefault).toHaveBeenCalled();
      resetModalStack();
    }
  });

  it("does NOT preventDefault unmatched non-reserved keys", () => {
    pushModalFrame("modal", []);
    const e = event({ key: "x" });
    dispatchKeydown(e, () => ctx);
    expect(e.preventDefault).not.toHaveBeenCalled();
  });

  it("skips a modal frame action whose binding matches but when() returns false", () => {
    // Regression coverage: previously a modal action with a matching key
    // would fire its handler regardless of when(), so a disabled action
    // (e.g. confirm-on-conflict gated by branchConflict) could still run.
    const handler = vi.fn();
    pushModalFrame("modal", [
      {
        id: "modal.disabled",
        label: "Disabled",
        binding: { key: "j" },
        priority: 100,
        when: () => false,
        handler,
      },
    ]);
    dispatchKeydown(event({ key: "j" }), () => ctx);
    expect(handler).not.toHaveBeenCalled();
  });
});

describe("dispatchKeydown — error handling", () => {
  beforeEach(() => {
    resetRegistry();
    resetModalStack();
  });

  it("routes async handler rejections to flash with the Error message", async () => {
    const flash = vi.spyOn(flashModule, "showFlash").mockImplementation(() => {});
    registerScopedActions("e", [
      {
        id: "fail",
        label: "Fail",
        scope: "global",
        binding: { key: "j" },
        priority: 0,
        when: () => true,
        handler: () => Promise.reject(new Error("boom")),
      },
    ]);
    dispatchKeydown(event({ key: "j" }), () => ctx);
    await new Promise((r) => setTimeout(r, 0));
    expect(flash).toHaveBeenCalledWith(expect.stringContaining("boom"), { tone: "danger" });
    flash.mockRestore();
  });
});

describe("dispatchKeydown — in-flight de-dup", () => {
  beforeEach(() => {
    resetRegistry();
    resetModalStack();
  });

  it("does not re-invoke an in-flight async action", async () => {
    let resolve!: () => void;
    const handler = vi.fn(
      () =>
        new Promise<void>((r) => {
          resolve = r;
        }),
    );
    registerScopedActions("a", [
      {
        id: "slow",
        label: "x",
        scope: "global",
        binding: { key: "j" },
        priority: 0,
        when: () => true,
        handler,
      },
    ]);
    dispatchKeydown(event({ key: "j" }), () => ctx);
    dispatchKeydown(event({ key: "j" }), () => ctx);
    expect(handler).toHaveBeenCalledTimes(1);
    resolve();
    await new Promise((r) => setTimeout(r, 0));
    dispatchKeydown(event({ key: "j" }), () => ctx);
    expect(handler).toHaveBeenCalledTimes(2);
  });
});

describe("dispatchKeydown — a focused terminal owns the keyboard", () => {
  beforeEach(() => {
    resetRegistry();
    resetModalStack();
    document.body.replaceChildren();
  });

  afterEach(() => {
    document.body.replaceChildren();
  });

  // The real chain, and both places focus actually lands: xterm parks it in a
  // hidden textarea, but a pane focused without a click into the grid holds it
  // on the session wrapper — which no editable selector matches.
  function terminalTargets(): { textarea: HTMLElement; wrapper: HTMLElement } {
    const wrapper = document.createElement("div");
    wrapper.dataset.sessionHost = "ws-1/agent";
    const container = document.createElement("div");
    container.className = "terminal-container";
    const xterm = document.createElement("div");
    xterm.className = "xterm";
    const textarea = document.createElement("textarea");
    textarea.className = "xterm-helper-textarea";
    xterm.append(textarea);
    container.append(xterm);
    wrapper.append(container);
    document.body.append(wrapper);
    return { textarea, wrapper };
  }

  function register(id: string, binding: Action["binding"]): ReturnType<typeof vi.fn> {
    const handler = vi.fn();
    registerScopedActions(id, [{ id, label: id, scope: "global", binding, priority: 0, when: () => true, handler }]);
    return handler;
  }

  it("leaves Escape, function keys, and Ctrl chords to the terminal", () => {
    const escape = register("escape.list", { key: "Escape" });
    const fnKey = register("help", { key: "F1" });
    const chord = register("palette", { key: "p", ctrlOrMeta: true });
    const { textarea, wrapper } = terminalTargets();

    for (const target of [textarea, wrapper]) {
      for (const init of [{ key: "Escape" }, { key: "F1" }, { key: "p", ctrlKey: true }]) {
        const e = event(init);
        Object.defineProperty(e, "target", { value: target });
        dispatchKeydown(e, () => ctx);
        expect(e.preventDefault, `${init.key} on ${target.tagName}`).not.toHaveBeenCalled();
      }
    }

    expect(escape).not.toHaveBeenCalled();
    expect(fnKey).not.toHaveBeenCalled();
    expect(chord).not.toHaveBeenCalled();
  });

  it("still dispatches the same keys away from a terminal", () => {
    const escape = register("escape.list", { key: "Escape" });
    const chord = register("palette", { key: "p", ctrlOrMeta: true });
    const outside = document.createElement("div");
    document.body.append(outside);

    for (const init of [{ key: "Escape" }, { key: "p", ctrlKey: true }]) {
      const e = event(init);
      Object.defineProperty(e, "target", { value: outside });
      dispatchKeydown(e, () => ctx);
    }

    expect(escape).toHaveBeenCalled();
    expect(chord).toHaveBeenCalled();
  });
});
