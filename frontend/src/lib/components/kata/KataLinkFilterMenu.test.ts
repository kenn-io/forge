import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { createKataLinkFilters } from "../../features/kata/kataLinkFilters.js";
import KataLinkFilterMenu from "./KataLinkFilterMenuRuntimeHarness.svelte";

describe("KataLinkFilterMenu", () => {
  afterEach(cleanup);

  it("emits independent task-state and relationship changes", async () => {
    const filters = createKataLinkFilters("open");
    const onChange = vi.fn();
    render(KataLinkFilterMenu, { props: { filters, onChange } });

    await fireEvent.click(screen.getByRole("button", { name: "Filter links" }));
    await fireEvent.click(screen.getByRole("checkbox", { name: "Closed" }));
    expect(onChange).toHaveBeenLastCalledWith({
      ...filters,
      statuses: { open: true, closed: true },
    });

    await fireEvent.click(screen.getByRole("checkbox", { name: "Blocked by" }));
    expect(onChange).toHaveBeenLastCalledWith({
      ...filters,
      relations: { ...filters.relations, blocked_by: false },
    });
  });

  it("closes on Escape and returns focus to the trigger", async () => {
    render(KataLinkFilterMenu, {
      props: { filters: createKataLinkFilters("all"), onChange: vi.fn() },
    });

    const trigger = screen.getByRole("button", { name: "Filter links" });
    await fireEvent.click(trigger);
    await fireEvent.keyDown(document, { key: "Escape" });

    expect(screen.queryByRole("group", { name: "Link filters" })).toBeNull();
    expect(document.activeElement).toBe(trigger);
  });
});
