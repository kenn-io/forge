import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it } from "vite-plus/test";

import LabelRow from "./LabelRow.svelte";

function getTooltipTrigger(container: HTMLElement): HTMLElement {
  const trigger = container.querySelector<HTMLElement>(".kit-tooltip-trigger");
  if (!trigger) throw new Error("expected a .kit-tooltip-trigger element");
  return trigger;
}

const labels = [
  { name: "bug", color: "d73a4a" },
  { name: "enhancement", color: "a2eeef" },
  { name: "docs", color: "0075ca" },
  { name: "help wanted", color: "008672" },
];

describe("LabelRow", () => {
  afterEach(() => {
    cleanup();
  });

  it("renders nothing without labels", () => {
    const { container } = render(LabelRow, { props: { labels: [] } });
    expect(container.querySelector(".label-row")).toBeNull();
  });

  it("renders every label in the default wrapping row", () => {
    render(LabelRow, { props: { labels } });
    for (const label of labels) {
      expect(screen.getByText(label.name)).toBeTruthy();
    }
    expect(screen.queryByText("+2")).toBeNull();
  });
});

describe("LabelRow dots variant", () => {
  afterEach(() => {
    cleanup();
  });

  it("renders nothing without labels", () => {
    const { container } = render(LabelRow, { props: { labels: [], dots: true } });
    expect(container.querySelector(".label-dots")).toBeNull();
  });

  it("renders one dot per label with a keyboard-reachable tooltip and sr-only text", async () => {
    const { container } = render(LabelRow, { props: { labels: labels.slice(0, 2), dots: true } });
    expect(container.querySelectorAll(".label-dot")).toHaveLength(2);
    expect(container.querySelector(".label-dots")?.getAttribute("aria-hidden")).toBe("true");
    expect(screen.getByText("Labels: bug, enhancement")).toBeTruthy();
    expect(screen.queryByText("bug")).toBeNull();

    const trigger = getTooltipTrigger(container);
    expect(trigger.getAttribute("tabindex")).toBe("0");
    expect(screen.queryByRole("tooltip")).toBeNull();

    await fireEvent.focusIn(trigger);
    const tooltip = screen.getByRole("tooltip");
    expect(tooltip.textContent).toBe("bug, enhancement");

    await fireEvent.focusOut(trigger);
    expect(screen.queryByRole("tooltip")).toBeNull();
  });

  it("caps dots at four with no overflow indicator, tooltip still lists all names", async () => {
    const five = [...labels, { name: "extra", color: "ffffff" }];
    const { container } = render(LabelRow, { props: { labels: five, dots: true } });
    expect(container.querySelectorAll(".label-dot")).toHaveLength(4);
    expect(screen.queryByText(/^\+\d+$/)).toBeNull();

    await fireEvent.focusIn(getTooltipTrigger(container));
    expect(screen.getByRole("tooltip").textContent).toBe("bug, enhancement, docs, help wanted, extra");
  });

  it("normalizes bare, prefixed, 3-digit, and invalid hex colors", () => {
    const { container } = render(LabelRow, {
      props: {
        labels: [
          { name: "bare", color: "d73a4a" },
          { name: "prefixed", color: "#A2EEEF" },
          { name: "short", color: "0aF" },
          { name: "invalid", color: "not-a-color" },
        ],
        dots: true,
      },
    });
    const styles = [...container.querySelectorAll(".label-dot")].map((n) => n.getAttribute("style") ?? "");
    expect(styles[0]).toContain("rgb(215, 58, 74)");
    expect(styles[1]).toContain("rgb(162, 238, 239)");
    expect(styles[2]).toContain("rgb(0, 170, 255)");
    expect(styles[3]).toContain("rgb(110, 119, 129)");
  });
});
