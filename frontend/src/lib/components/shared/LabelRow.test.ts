import { cleanup, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it } from "vite-plus/test";

import LabelRow from "./LabelRow.svelte";

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

  it("compact rows show the first two labels plus a passive overflow count", () => {
    render(LabelRow, { props: { labels, compact: true } });
    expect(screen.getByText("bug")).toBeTruthy();
    expect(screen.getByText("enhancement")).toBeTruthy();
    expect(screen.queryByText("docs")).toBeNull();
    expect(screen.getByText("+2")).toBeTruthy();
  });

  it("compact rows with two labels have no overflow count", () => {
    render(LabelRow, { props: { labels: labels.slice(0, 2), compact: true } });
    expect(screen.getByText("bug")).toBeTruthy();
    expect(screen.getByText("enhancement")).toBeTruthy();
    expect(screen.queryByText(/^\+\d+$/)).toBeNull();
  });
});
