import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import LabelPicker from "../../../../../packages/ui/src/components/detail/LabelPicker.svelte";
import type { Label } from "@middleman/ui/api/types";

function label(name: string, description = ""): Label {
  return {
    name,
    description,
    color: name === "bug" ? "d73a4a" : "fbca04",
    is_default: false,
  };
}

describe("LabelPicker", () => {
  afterEach(() => cleanup());

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
