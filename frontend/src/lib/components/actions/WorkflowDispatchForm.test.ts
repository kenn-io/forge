import { fireEvent, render, screen } from "@testing-library/svelte";
import { describe, expect, it, vi } from "vitest";
import type { components } from "../../api/generated/schema.js";
import type { OperationAvailability } from "../../api/types.js";
import WorkflowDispatchForm from "./WorkflowDispatchForm.svelte";

type Workflow = components["schemas"]["WorkflowDefinitionResponse"];
type Environment = components["schemas"]["WorkflowEnvironmentResponse"];

const environments: Environment[] = [{ name: "staging" }, { name: "production" }];
const available: OperationAvailability = { available: true };

function workflow(inputs: NonNullable<Workflow["inputs"]> = []): Workflow {
  return {
    available: true,
    definition_sha: "definition-1",
    id: "deploy.yml",
    inputs,
    name: "Deploy",
    path: ".github/workflows/deploy.yml",
    state: "active",
    web_url: "https://github.com/acme/app/actions/workflows/deploy.yml",
  };
}

describe("WorkflowDispatchForm", () => {
  it("renders typed defaults and provider option order", () => {
    render(WorkflowDispatchForm, {
      workflow: workflow([
        { name: "message", type: "string", required: false, has_default: true, default: "hello" },
        { name: "retries", type: "number", required: false, has_default: true, default: 3 },
        { name: "dry_run", type: "boolean", required: false, has_default: true, default: true },
        { name: "region", type: "choice", required: false, has_default: true, default: "eu", options: ["eu", "us"] },
        { name: "target", type: "environment", required: false, has_default: false },
      ]),
      environments,
      initialRef: "main",
      operation: available,
      state: { kind: "idle" },
      onsubmit: vi.fn(),
    });

    expect((screen.getByRole("textbox", { name: "message" }) as HTMLInputElement).value).toBe("hello");
    expect((screen.getByRole("spinbutton", { name: "retries" }) as HTMLInputElement).valueAsNumber).toBe(3);
    expect((screen.getByRole("checkbox", { name: "dry_run" }) as HTMLInputElement).checked).toBe(true);
    expect(screen.getByRole("combobox", { name: "region" }).querySelectorAll("option")).toHaveLength(2);
    expect([...screen.getByRole("combobox", { name: "region" }).querySelectorAll("option")].map((option) => option.textContent)).toEqual(["eu", "us"]);
    expect([...screen.getByRole("combobox", { name: "target" }).querySelectorAll("option")].map((option) => option.textContent)).toEqual(["Select an environment", "staging", "production"]);
  });

  it("validates required inputs and editable ref, then submits one normalized request", async () => {
    const onsubmit = vi.fn();
    render(WorkflowDispatchForm, {
      workflow: workflow([
        { name: "version", type: "string", required: true, has_default: false },
        { name: "count", type: "number", required: true, has_default: false },
        { name: "approved", type: "boolean", required: false, has_default: false },
      ]),
      environments,
      initialRef: "main",
      operation: available,
      state: { kind: "idle" },
      onsubmit,
    });

    const ref = screen.getByRole("textbox", { name: "Git ref" });
    expect((ref as HTMLInputElement).value).toBe("main");
    await fireEvent.input(ref, { target: { value: "" } });
    await fireEvent.click(screen.getByRole("button", { name: "Run workflow" }));
    expect((await screen.findByText("Git ref is required.")).getAttribute("role")).toBe("alert");
    expect(screen.getByText("version is required.")).toBeTruthy();
    expect(screen.getByText("count is required.")).toBeTruthy();
    expect(onsubmit).not.toHaveBeenCalled();

    await fireEvent.input(ref, { target: { value: " feature/test " } });
    await fireEvent.input(screen.getByRole("textbox", { name: "version" }), { target: { value: " v2 " } });
    await fireEvent.input(screen.getByRole("spinbutton", { name: "count" }), { target: { value: "4" } });
    await fireEvent.click(screen.getByRole("checkbox", { name: "approved" }));
    await fireEvent.click(screen.getByRole("button", { name: "Run workflow" }));
    expect(onsubmit).toHaveBeenCalledTimes(1);
    expect(onsubmit).toHaveBeenCalledWith({ ref: "feature/test", inputs: { version: "v2", count: 4, approved: true } });
  });

  it("surfaces unavailable, pending, uncertain, and conflict states without duplicate dispatch", async () => {
    const onsubmit = vi.fn();
    const props = {
      workflow: workflow(), environments, initialRef: "main", onsubmit,
      operation: { available: false, unavailable_reason: "No write credential" } as OperationAvailability,
      state: { kind: "idle" } as const,
    };
    const view = render(WorkflowDispatchForm, props);
    expect(screen.getByText("No write credential")).toBeTruthy();
    expect((screen.getByRole("button", { name: "Run workflow" }) as HTMLButtonElement).disabled).toBe(true);

    await view.rerender({ ...props, operation: available, state: { kind: "pending" } });
    expect((screen.getByRole("textbox", { name: "Git ref" }) as HTMLInputElement).disabled).toBe(true);
    expect((screen.getByRole("button", { name: "Running workflow…" }) as HTMLButtonElement).disabled).toBe(true);
    await fireEvent.click(screen.getByRole("button", { name: "Running workflow…" }));
    expect(onsubmit).not.toHaveBeenCalled();

    await view.rerender({ ...props, operation: available, state: { kind: "uncertain", message: "The provider may have accepted this run. Verify on the provider before trying again." } });
    expect(screen.getByRole("alert").textContent).toContain("may have accepted");
    expect((screen.getByRole("button", { name: "Run workflow" }) as HTMLButtonElement).disabled).toBe(true);

    await view.rerender({ ...props, operation: available, state: { kind: "conflict" } });
    expect(screen.getByRole("alert").textContent).toContain("Workflow definition changed. Reload workflows before running it.");
    expect(screen.queryByRole("textbox", { name: "Git ref" })).toBeNull();
  });

  it("requires explicit confirmation even when the workflow has no inputs", async () => {
    const onsubmit = vi.fn();
    render(WorkflowDispatchForm, { workflow: workflow(), environments, initialRef: "release", operation: available, state: { kind: "idle" }, onsubmit });
    expect(screen.getByRole("heading", { name: "Deploy" })).toBeTruthy();
    expect((screen.getByRole("textbox", { name: "Git ref" }) as HTMLInputElement).value).toBe("release");
    expect(screen.getByRole("button", { name: "Run workflow" })).toBeTruthy();
    expect(onsubmit).not.toHaveBeenCalled();
  });
});
