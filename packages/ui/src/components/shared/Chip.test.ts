import { cleanup, render, screen } from "@testing-library/svelte";
import { createRawSnippet } from "svelte";
import { afterEach, describe, expect, it } from "vite-plus/test";

import Chip from "./Chip.svelte";

describe("Chip", () => {
  afterEach(() => {
    cleanup();
  });

  it("expresses semantic tone and dot state without caller-owned classes", () => {
    render(Chip, {
      props: {
        tone: "success",
        dot: true,
        uppercase: false,
        children: createRawSnippet(() => ({
          render: () => "<span>Running</span>",
        })),
      },
    });

    const chip = screen.getByText("Running").closest(".chip");
    expect(chip).not.toBeNull();
    expect(chip?.classList.contains("chip--tone-success")).toBe(true);
    expect(chip?.querySelector(".chip__dot")).not.toBeNull();
  });

  it("wraps content in a truncation label", () => {
    render(Chip, {
      props: {
        children: createRawSnippet(() => ({
          render: () => "<span>acme/widgets</span>",
        })),
      },
    });

    const chip = screen.getByText("acme/widgets").closest(".chip");
    expect(chip?.querySelector(".chip__label")?.textContent).toBe("acme/widgets");
  });
});
