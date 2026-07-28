import { flushSync, mount, unmount } from "svelte";
import { describe, expect, it, vi } from "vite-plus/test";
import { createSettingsStore } from "@middleman/ui";
import { STORES_KEY } from "../../../../../packages/ui/src/context.js";
import XtermTerminalPane from "./XtermTerminalPane.svelte";

describe("XtermTerminalPane focus", () => {
  it("moves keyboard focus into the terminal when it is created active", async () => {
    const target = document.createElement("div");
    target.style.width = "600px";
    target.style.height = "400px";
    document.body.appendChild(target);
    const props = $state({
      websocketPath: "/api/v1/workspaces/ws-1/runtime/sessions/s1/attach",
      active: true,
    });
    const component = mount(XtermTerminalPane, {
      target,
      props,
      context: new Map([[STORES_KEY, { settings: createSettingsStore() }]]),
    });
    try {
      await vi.waitFor(() => {
        expect(target.querySelector(".xterm-helper-textarea")).not.toBeNull();
      });
      await vi.waitFor(() => {
        expect(document.activeElement).toBe(target.querySelector(".xterm-helper-textarea"));
      });
    } finally {
      unmount(component);
      target.remove();
    }
  });

  it("constructs without stealing focus when autofocus is disabled", async () => {
    const target = document.createElement("div");
    target.style.width = "600px";
    target.style.height = "400px";
    document.body.appendChild(target);
    const button = document.createElement("button");
    document.body.appendChild(button);
    button.focus();
    const props = $state({
      websocketPath: "/api/v1/workspaces/ws-1/runtime/sessions/s1/attach",
      active: true,
      autoFocus: false,
    });
    const component = mount(XtermTerminalPane, {
      target,
      props,
      context: new Map([[STORES_KEY, { settings: createSettingsStore() }]]),
    });
    try {
      await vi.waitFor(() => {
        expect(target.querySelector(".xterm-helper-textarea")).not.toBeNull();
      });
      expect(document.activeElement).toBe(button);
    } finally {
      unmount(component);
      target.remove();
      button.remove();
    }
  });

  it("does not steal focus when an already-mounted terminal becomes active", async () => {
    const target = document.createElement("div");
    target.style.width = "600px";
    target.style.height = "400px";
    document.body.appendChild(target);
    const button = document.createElement("button");
    document.body.appendChild(button);
    const props = $state({
      websocketPath: "/api/v1/workspaces/ws-1/runtime/sessions/s1/attach",
      active: false,
    });
    const component = mount(XtermTerminalPane, {
      target,
      props,
      context: new Map([[STORES_KEY, { settings: createSettingsStore() }]]),
    });
    try {
      await vi.waitFor(() => {
        expect(target.querySelector(".xterm-helper-textarea")).not.toBeNull();
      });
      button.focus();
      expect(document.activeElement).toBe(button);

      props.active = true;
      flushSync();
      await vi.waitFor(() => {
        expect(target.querySelector(".xterm-helper-textarea")).not.toBeNull();
      });
      expect(document.activeElement).toBe(button);
    } finally {
      unmount(component);
      target.remove();
      button.remove();
    }
  });

  it("does not steal focus from a button focused during the async init window", async () => {
    const target = document.createElement("div");
    target.style.width = "600px";
    target.style.height = "400px";
    document.body.appendChild(target);
    const button = document.createElement("button");
    document.body.appendChild(button);
    const props = $state({
      websocketPath: "/api/v1/workspaces/ws-1/runtime/sessions/s1/attach",
      active: true,
    });
    const component = mount(XtermTerminalPane, {
      target,
      props,
      context: new Map([[STORES_KEY, { settings: createSettingsStore() }]]),
    });
    try {
      // Mount captures the focus intent synchronously, before the font-load
      // race in start() resolves — focusing the button here lands inside
      // that async window, the same way live-remounting under an open
      // settings popover would.
      button.focus();
      expect(document.activeElement).toBe(button);

      await vi.waitFor(() => {
        expect(target.querySelector(".xterm-helper-textarea")).not.toBeNull();
      });
      expect(document.activeElement).toBe(button);
    } finally {
      unmount(component);
      target.remove();
      button.remove();
    }
  });

  it("does not steal focus from a control inside an open dialog", async () => {
    const target = document.createElement("div");
    target.style.width = "600px";
    target.style.height = "400px";
    document.body.appendChild(target);
    const dialog = document.createElement("div");
    dialog.setAttribute("role", "dialog");
    const dialogInput = document.createElement("input");
    dialog.appendChild(dialogInput);
    document.body.appendChild(dialog);
    dialogInput.focus();
    expect(document.activeElement).toBe(dialogInput);

    const props = $state({
      websocketPath: "/api/v1/workspaces/ws-1/runtime/sessions/s1/attach",
      active: true,
    });
    const component = mount(XtermTerminalPane, {
      target,
      props,
      context: new Map([[STORES_KEY, { settings: createSettingsStore() }]]),
    });
    try {
      await vi.waitFor(() => {
        expect(target.querySelector(".xterm-helper-textarea")).not.toBeNull();
      });
      expect(document.activeElement).toBe(dialogInput);
    } finally {
      unmount(component);
      target.remove();
      dialog.remove();
    }
  });
});
