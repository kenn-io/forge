import { cleanup, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it } from "vite-plus/test";

import HeaderIconButton from "./HeaderIconButton.svelte";

describe("HeaderIconButton", () => {
  afterEach(() => {
    cleanup();
  });

  it("renders an icon-only header button contract", () => {
    render(HeaderIconButton, {
      props: {
        title: "Toggle theme",
        active: true,
      },
    });

    const button = screen.getByTitle("Toggle theme");

    expect(button.getAttribute("type")).toBe("button");
    expect(button.getAttribute("data-active")).toBe("true");
    expect(button.getAttribute("aria-label")).toBe("Toggle theme");
  });
});
