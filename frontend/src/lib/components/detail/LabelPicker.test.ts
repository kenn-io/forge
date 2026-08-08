import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import type { Label } from "../../api/types.js";
import LabelPicker from "./LabelPicker.svelte";

function label(name: string, description = ""): Label {
  return {
    name,
    description,
    color: name === "bug" ? "d73a4a" : "fbca04",
    is_default: false,
  };
}

const catalog: Label[] = [
  { name: "bug", color: "d73a4a", description: "Something is broken" },
  { name: "docs", color: "0075ca", description: "" },
];

const selected: Label[] = [catalog[0]!];

afterEach(() => {
  cleanup();
});

describe("LabelPicker", () => {
  it("filters catalog labels and marks assigned labels checked", async () => {
    render(LabelPicker, {
      props: {
        catalogLabels: [label("bug", "Broken behavior"), label("triage", "Needs review")],
        selectedLabels: [label("bug")],
        syncing: false,
        pendingLabel: null,
        error: null,
        ontoggle: vi.fn(),
        onclose: vi.fn(),
      },
    });

    expect(screen.getByRole("menuitemcheckbox", { name: /bug/i }).getAttribute("aria-checked")).toBe("true");
    expect(screen.getByRole("menuitemcheckbox", { name: /triage/i }).getAttribute("aria-checked")).toBe("false");

    await fireEvent.input(screen.getByLabelText("Filter labels"), {
      target: { value: "tri" },
    });

    expect(screen.queryByRole("menuitemcheckbox", { name: /bug/i })).toBeNull();
    expect(screen.getByRole("menuitemcheckbox", { name: /triage/i })).toBeTruthy();
  });

  it("emits the toggled label name", async () => {
    const onToggle = vi.fn();
    render(LabelPicker, {
      props: {
        catalogLabels: [label("bug"), label("triage")],
        selectedLabels: [label("bug")],
        syncing: false,
        pendingLabel: null,
        error: null,
        ontoggle: onToggle,
        onclose: vi.fn(),
      },
    });

    await fireEvent.click(screen.getByRole("menuitemcheckbox", { name: /triage/i }));

    expect(onToggle).toHaveBeenCalledWith("triage");
  });

  it("emits clear from the header action", async () => {
    const onClear = vi.fn();
    render(LabelPicker, {
      props: {
        catalogLabels: [label("bug"), label("triage")],
        selectedLabels: [label("bug")],
        syncing: false,
        pendingLabel: null,
        error: null,
        ontoggle: vi.fn(),
        onclear: onClear,
        onclose: vi.fn(),
      },
    });

    await fireEvent.click(screen.getByRole("button", { name: "Clear selected labels" }));

    expect(onClear).toHaveBeenCalledOnce();
  });
});

// The picker can be open when label operations become unavailable
// (e.g. operation availability refreshes while the popover is up), so
// the mutation controls inside it — toggle rows and clear — must
// disable and stop invoking the mutation callbacks. Closing stays
// possible.
describe("LabelPicker disabled gate", () => {
  it("disables toggle rows and clear with the reason and suppresses callbacks", async () => {
    const ontoggle = vi.fn();
    const onclear = vi.fn();
    render(LabelPicker, {
      props: {
        catalogLabels: catalog,
        selectedLabels: selected,
        disabled: true,
        disabledReason: "No user credential for writes on github.com",
        ontoggle,
        onclear,
        onclose: () => {},
      },
    });

    const row = screen.getByRole("menuitemcheckbox", { name: /bug/i }) as HTMLButtonElement;
    expect(row.disabled).toBe(true);
    expect(row.title).toBe("No user credential for writes on github.com");
    row.click();
    expect(ontoggle).not.toHaveBeenCalled();

    const clear = screen.getByRole("button", { name: "Clear selected labels" }) as HTMLButtonElement;
    expect(clear.disabled).toBe(true);
    expect(clear.title).toBe("No user credential for writes on github.com");
    clear.click();
    expect(onclear).not.toHaveBeenCalled();
  });

  it("keeps toggle and clear live when not disabled", async () => {
    const ontoggle = vi.fn();
    const onclear = vi.fn();
    render(LabelPicker, {
      props: {
        catalogLabels: catalog,
        selectedLabels: selected,
        ontoggle,
        onclear,
        onclose: () => {},
      },
    });

    const row = screen.getByRole("menuitemcheckbox", { name: /docs/i }) as HTMLButtonElement;
    expect(row.disabled).toBe(false);
    row.click();
    expect(ontoggle).toHaveBeenCalledWith("docs");

    const clear = screen.getByRole("button", { name: "Clear selected labels" }) as HTMLButtonElement;
    expect(clear.disabled).toBe(false);
    clear.click();
    expect(onclear).toHaveBeenCalled();
  });
});
