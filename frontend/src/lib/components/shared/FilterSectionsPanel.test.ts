import { FilterDropdown, type FilterDropdownSection } from "@kenn-io/kit-ui";
import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { createRawSnippet } from "svelte";
import { afterEach, describe, expect, it } from "vite-plus/test";

import FilterSectionsPanel from "./FilterSectionsPanel.svelte";

// No list menu sets a count, description, or plain-row detail today. The phone
// panel must still show whatever FilterDropdown shows, so a filter that gains
// one does not quietly look different on the phone.
const submenu = createRawSnippet(() => ({ render: () => "<div>choices</div>" }));
const sections: FilterDropdownSection[] = [
  {
    title: "Section",
    items: [
      {
        id: "counted",
        label: "Counted",
        count: 0,
        detail: "none yet",
        description: "Counts rows",
        active: false,
        onSelect: () => {},
      },
      { id: "plain", label: "Plain", active: true, onSelect: () => {} },
      {
        id: "nested",
        label: "Nested",
        detail: "2 selected",
        description: "Opens choices",
        active: true,
        submenu,
        onSelect: () => {},
      },
    ],
  },
];

interface RowValues {
  label: string;
  count: string;
  detail: string;
  description: string;
  /** What a screen reader announces as the row's description. */
  describedBy: string;
}

function rowValues(rows: Element[], prefix: string): RowValues[] {
  const text = (row: Element, part: string) => row.querySelector(`.${prefix}__${part}`)?.textContent?.trim() ?? "";
  return rows.map((row) => ({
    label: text(row, "label"),
    count: text(row, "count"),
    detail: text(row, "detail"),
    description: row.getAttribute("title") ?? "",
    describedBy: document.getElementById(row.getAttribute("aria-describedby") ?? "")?.textContent?.trim() ?? "",
  }));
}

describe("FilterSectionsPanel", () => {
  afterEach(cleanup);

  it("shows each row's count, detail, and description as FilterDropdown does", async () => {
    const dropdown = render(FilterDropdown, { props: { label: "Filters", sections } });
    await fireEvent.click(screen.getByRole("button", { name: "Filters" }));
    const desktop = rowValues([...document.querySelectorAll(".kit-filter-dropdown__item")], "kit-filter-dropdown");
    dropdown.unmount();

    const { container } = render(FilterSectionsPanel, {
      props: { sections, resetLabel: "Reset view", onReset: () => {} },
    });
    const phone = rowValues([...container.querySelectorAll(".filter-sections-panel__item")], "filter-sections-panel");

    expect(desktop.map((row) => row.label)).toEqual(["Counted", "Plain", "Nested"]);
    expect(desktop.map((row) => row.describedBy)).toEqual(["Counts rows", "", "Opens choices"]);
    expect(phone).toEqual(desktop);
  });
});
