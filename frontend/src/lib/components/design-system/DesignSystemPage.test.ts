import { cleanup, render, screen } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import DesignSystemPage from "./DesignSystemPage.svelte";

describe("DesignSystemPage", () => {
  beforeEach(() => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => Response.json([])),
    );
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("places the onboarding prototype gallery on the design-system surface", () => {
    render(DesignSystemPage);

    expect(screen.getByRole("heading", { name: "Onboarding prototypes" })).toBeTruthy();
    expect(screen.getByRole("tablist", { name: "Onboarding prototype" })).toBeTruthy();
  });
});
