import type { Component } from "svelte";
import { afterEach, expect, it, vi } from "vite-plus/test";
import { cleanup, render } from "vitest-browser-svelte";

import "../../../app.css";
import AppRuntimeHarness from "../../../test/AppRuntimeHarness.svelte";
import ExternalContextCards from "./ExternalContextCards.svelte";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

it("keeps the PR layout steady while an external source determines it is not applicable", async () => {
  const initial = Promise.withResolvers<void>();
  const refresh = Promise.withResolvers<void>();
  let reads = 0;
  let responses = 0;
  vi.stubGlobal("fetch", async (request: Request) => {
    if (request.url.endsWith("/external-context/sources")) {
      return Response.json({ sources: [{ id: "checks", name: "Quality checks" }] });
    }
    if (request.url.includes("/external-context/checks")) {
      await (reads++ === 0 ? initial.promise : refresh.promise);
      responses++;
      return Response.json({ card: null });
    }
    throw new Error(`Unexpected request: ${request.url}`);
  });
  const target = document.createElement("div");
  const description = document.createElement("p");
  description.textContent = "Pull request description";
  document.body.append(target, description);
  try {
    await document.fonts.ready;
    const top = description.getBoundingClientRect().top;
    const props = {
      component: ExternalContextCards as Component,
      ref: { provider: "github", owner: "example", name: "project", repoPath: "example/project" },
      repositoryKey: { kind: "id", id: 123 },
      number: 42,
      headSha: "a".repeat(40),
    };
    const view = await render(AppRuntimeHarness, { target, props });
    await expect.poll(() => reads).toBe(1);
    expect(description.getBoundingClientRect().top).toBe(top);
    expect(target.querySelector(".external-context")).toBeNull();

    initial.resolve();
    await expect.poll(() => responses).toBe(1);
    expect(description.getBoundingClientRect().top).toBe(top);

    // A normal detail refresh supplies a new reference for the same PR.
    await view.rerender({ ...props, ref: { ...props.ref } });
    await expect.poll(() => reads).toBeGreaterThan(1);
    expect(description.getBoundingClientRect().top).toBe(top);
    expect(target.querySelector(".external-context")).toBeNull();
    refresh.resolve();
    await expect.poll(() => responses === reads).toBe(true);
    expect(target.querySelector(".external-context")).toBeNull();
    expect(description.getBoundingClientRect().top).toBe(top);
  } finally {
    cleanup();
    target.remove();
    description.remove();
  }
});
