import { expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";
import { render } from "vitest-browser-svelte";
import "../../../app.css";
import PierreFileTree from "./PierreFileTreeRuntimeHarness.svelte";

it.each([1, 1.1, 1.25])("only truncates overflowing filenames at %s scaling", async (zoom) => {
  await page.viewport(640, 500);
  const shortPaths = ["docs/guide/SKILL.md", "docs/guide/agents/config.yaml", "docs/guide/references/patterns.md"];
  const longPath = `docs/guide/${"long-filename-".repeat(10)}.md`;
  render(PierreFileTree, {
    props: {
      files: [],
      entries: [...shortPaths, longPath].map((path) => ({ path, decoration: "2w" })),
    },
  });
  const host = document.querySelector<HTMLElement>(".diff-file-tree");
  if (!host) throw new Error("missing tree host");
  host.style.height = "400px";
  host.style.width = "480px";
  host.style.zoom = String(zoom);
  await document.fonts.ready;
  await vi.waitFor(() => {
    expect(host.shadowRoot?.querySelectorAll("[data-item-type='file']").length).toBe(4);
  });
  const root = host.shadowRoot;
  if (!root) throw new Error("missing tree shadow root");
  // Disable marker transitions so the assertion measures settled overflow.
  const style = document.createElement("style");
  style.textContent = "[data-truncate-marker] { transition: none; }";
  root.append(style);
  await new Promise(requestAnimationFrame);
  await new Promise(requestAnimationFrame);

  for (const path of [...shortPaths, longPath]) {
    const row = root.querySelector<HTMLElement>(`[data-item-path="${path}"]`);
    if (!row) throw new Error(`missing row ${path}`);
    const markers = [...row.querySelectorAll<HTMLElement>("[data-truncate-marker]")];
    expect(markers.length).toBeGreaterThan(0);
    const showsEllipsis = markers.some((marker) => Number(getComputedStyle(marker).opacity) > 0);
    expect(showsEllipsis, path).toBe(path === longPath);
    if (path === longPath) continue;
    const age = row.querySelector<HTMLElement>("[data-item-section='decoration']");
    if (!age) throw new Error(`missing age ${path}`);
    expect(age.getBoundingClientRect().right).toBeLessThanOrEqual(row.getBoundingClientRect().right);
    expect(age.clientWidth).toBeGreaterThan(0);
  }
});
