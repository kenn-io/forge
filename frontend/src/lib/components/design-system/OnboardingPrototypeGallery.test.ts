import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it } from "vite-plus/test";

import OnboardingPrototypeGallery from "./OnboardingPrototypeGallery.svelte";

describe("OnboardingPrototypeGallery", () => {
  afterEach(cleanup);

  it("switches between the three onboarding directions", async () => {
    render(OnboardingPrototypeGallery);

    const wizard = screen.getByRole("tab", { name: "Focused setup" });
    expect(wizard.getAttribute("aria-selected")).toBe("true");
    expect(
      screen.getByRole("heading", {
        name: "Choose the repositories you maintain",
      }),
    ).toBeTruthy();

    const checklist = screen.getByRole("tab", {
      name: "Activation checklist",
    });
    await fireEvent.click(checklist);
    expect(checklist.getAttribute("aria-selected")).toBe("true");
    expect(screen.getByRole("heading", { name: "Your first useful session" })).toBeTruthy();

    const guide = screen.getByRole("tab", { name: "Start guide" });
    await fireEvent.click(guide);
    expect(guide.getAttribute("aria-selected")).toBe("true");
    expect(screen.getByRole("heading", { name: "From gh to a working PR" })).toBeTruthy();
  });

  it("filters wizard repositories and advances to first sync", async () => {
    render(OnboardingPrototypeGallery);

    await fireEvent.input(screen.getByRole("searchbox", { name: "Filter repositories" }), {
      target: { value: "docs" },
    });
    expect(screen.getByText("acme/docs")).toBeTruthy();
    expect(screen.queryByText("acme/forge")).toBeNull();

    await fireEvent.click(screen.getByRole("button", { name: /Configure 1 repository/ }));
    expect(screen.getByRole("heading", { name: "First sync is underway" })).toBeTruthy();
  });

  it("activates repository setup from the in-shell checklist", async () => {
    render(OnboardingPrototypeGallery);

    await fireEvent.click(screen.getByRole("tab", { name: "Activation checklist" }));
    await fireEvent.click(screen.getByRole("button", { name: "Choose repositories" }));
    expect(screen.getByRole("heading", { name: "Select repositories" })).toBeTruthy();
  });

  it("moves the start guide between activation tasks", async () => {
    render(OnboardingPrototypeGallery);

    await fireEvent.click(screen.getByRole("tab", { name: "Start guide" }));
    await fireEvent.click(screen.getByRole("button", { name: "2. Add repositories" }));
    expect(
      screen.getByRole("heading", {
        name: "Choose what Kenn Forge should track",
      }),
    ).toBeTruthy();
  });
});
