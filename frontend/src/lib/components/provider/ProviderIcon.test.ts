import { cleanup, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it } from "vite-plus/test";

import ProviderIcon from "./ProviderIcon.svelte";

describe("ProviderIcon", () => {
  afterEach(() => {
    cleanup();
  });

  it("renders known provider brand icons with accessible labels", () => {
    render(ProviderIcon, { props: { provider: "github" } });
    expect(screen.getByRole("img", { name: "GitHub" })).toBeTruthy();
  });

  it("renders nothing for unknown providers", () => {
    const { container } = render(ProviderIcon, {
      props: { provider: "unknown" },
    });
    expect(container.querySelector("svg")).toBeNull();
  });
});
