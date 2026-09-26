// Operator highlights are drawn in a layer behind the native input, so they
// only mean anything if they line up with the input's own glyphs. jsdom has no
// layout, so alignment is asserted here against real Chromium text metrics.

import { describe, expect, it } from "vite-plus/test";
import { render } from "vitest-browser-svelte";

import "../../../app.css";

import QuerySearchInput from "./QuerySearchInput.svelte";

const nextFrame = () => new Promise((resolve) => requestAnimationFrame(resolve));

// Measures text through a hidden copy of the input itself, so the ruler uses
// the input's real text rendering rather than canvas font approximations.
function textWidth(input: HTMLInputElement, text: string): number {
  const probe = input.cloneNode() as HTMLInputElement;
  probe.value = text;
  probe.style.cssText = "position:absolute; visibility:hidden; width:0; flex:none; padding:0";
  input.parentElement!.append(probe);
  const width = probe.scrollWidth;
  probe.remove();
  return width;
}

async function renderSearch(value: string, block: boolean) {
  const { container } = await render(QuerySearchInput, {
    value,
    size: "sm",
    block,
    ariaLabel: "Search",
  });
  await nextFrame();
  await nextFrame();
  const input = container.querySelector<HTMLInputElement>("input")!;
  const marks = Array.from(container.querySelectorAll<HTMLElement>(".query-field__operator"));
  return { container, input, marks };
}

describe("QuerySearchInput (browser)", () => {
  it("draws each operator tint under the matching input characters", async () => {
    const value = "fix NOT alice !bob";
    const { container, input, marks } = await renderSearch(value, true);

    expect(marks.map((mark) => mark.textContent)).toEqual(["NOT", "!"]);
    const inputLeft = input.getBoundingClientRect().left;
    for (const [mark, start] of [
      [marks[0]!, value.indexOf("NOT")],
      [marks[1]!, value.indexOf("!")],
    ] as const) {
      const rect = mark.getBoundingClientRect();
      expect(Math.abs(rect.left - (inputLeft + textWidth(input, value.slice(0, start))))).toBeLessThanOrEqual(1);
      expect(Math.abs(rect.width - textWidth(input, mark.textContent!))).toBeLessThanOrEqual(1);
    }
    // The input must paint above the tint so its text and caret stay on top.
    // The layer ignores pointers, so make it hit-testable to probe paint order.
    const layer = container.querySelector<HTMLElement>(".query-field__layer")!;
    layer.style.pointerEvents = "auto";
    marks[0]!.style.pointerEvents = "auto";
    const rect = marks[0]!.getBoundingClientRect();
    expect(document.elementFromPoint(rect.left + rect.width / 2, rect.top + rect.height / 2)).toBe(input);
  });

  it("follows the input's horizontal scroll for long queries", async () => {
    const value = `${"word ".repeat(40)}NOT tail`;
    const { input, marks } = await renderSearch(value, false);
    expect(input.scrollWidth).toBeGreaterThan(input.clientWidth);

    input.scrollLeft = input.scrollWidth;
    input.dispatchEvent(new Event("scroll"));
    await nextFrame();
    await nextFrame();

    const inputRect = input.getBoundingClientRect();
    const markRect = marks[0]!.getBoundingClientRect();
    const expectedLeft = inputRect.left + textWidth(input, value.slice(0, value.indexOf("NOT"))) - input.scrollLeft;
    expect(Math.abs(markRect.left - expectedLeft)).toBeLessThanOrEqual(1);
    expect(markRect.left).toBeGreaterThanOrEqual(inputRect.left);
    expect(markRect.right).toBeLessThanOrEqual(inputRect.right);
  });
});
