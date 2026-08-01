import { cleanup, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it } from "vite-plus/test";

import DesignSystemPage from "./DesignSystemPage.svelte";

describe("DesignSystemPage", () => {
  afterEach(cleanup);

  it("places the onboarding prototype gallery on the design-system surface", () => {
    render(DesignSystemPage);

    expect(screen.getByRole("heading", { name: "Onboarding prototypes" })).toBeTruthy();
    expect(screen.getByRole("tablist", { name: "Onboarding prototype" })).toBeTruthy();
  });
});
