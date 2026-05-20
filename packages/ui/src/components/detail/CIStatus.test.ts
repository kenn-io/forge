import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import CIStatus from "./CIStatus.svelte";
import { __resetCIWarnings } from "../../utils/ci-buckets-warn.js";

const chipBaseProps = {
  status: "",
  detailLoaded: true,
  detailSyncing: false,
  owner: "acme",
  name: "widgets",
  number: 1,
  prKey: "ext-1",
};

function mkCheck(
  partial: Partial<{
    name: string;
    status: string;
    conclusion: string;
    duration_seconds: number;
    url: string;
    app: string;
  }> = {},
) {
  return {
    name: "x",
    status: "completed",
    conclusion: "success",
    url: "",
    app: "",
    ...partial,
  };
}

describe("CIStatus", () => {
  beforeEach(() => {
    __resetCIWarnings();
  });

  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
  });

  it("renders animated pending row icon while syncing", () => {
    render(CIStatus, {
      props: {
        ...chipBaseProps,
        status: "pending",
        checksJSON: JSON.stringify([
          mkCheck({ name: "build", status: "in_progress", conclusion: "" }),
          mkCheck({ name: "lint" }),
        ]),
        detailLoaded: true,
        detailSyncing: true,
        expanded: true,
      },
    });

    // One pending row should render with an animated spinner wrapper.
    const pendingSection = document.querySelector(
      "[data-testid='ci-section-pending']",
    );
    expect(pendingSection).not.toBeNull();
    expect(pendingSection!.querySelectorAll(".ci-row")).toHaveLength(1);
    expect(pendingSection!.querySelector(".spin")).not.toBeNull();
  });

  it("keeps the pending row icon after refresh settles", () => {
    render(CIStatus, {
      props: {
        ...chipBaseProps,
        status: "pending",
        checksJSON: JSON.stringify([
          mkCheck({ name: "build", status: "in_progress", conclusion: "" }),
        ]),
        detailLoaded: true,
        detailSyncing: false,
        expanded: true,
      },
    });

    const pendingSection = document.querySelector(
      "[data-testid='ci-section-pending']",
    );
    expect(pendingSection).not.toBeNull();
    expect(pendingSection!.querySelectorAll(".ci-row")).toHaveLength(1);
    expect(pendingSection!.querySelector(".spin")).not.toBeNull();
  });

  it("renders expanded CI checks when chip is clicked", async () => {
    render(CIStatus, {
      props: {
        ...chipBaseProps,
        status: "success",
        checksJSON: JSON.stringify([
          mkCheck({
            name: "build",
            url: "https://example.com/build",
            app: "GitHub Actions",
            duration_seconds: 135,
          }),
          mkCheck({
            name: "test",
            url: "https://example.com/test",
            app: "GitHub Actions",
          }),
          mkCheck({
            name: "lint",
            url: "https://example.com/lint",
            app: "GitHub Actions",
          }),
          mkCheck({
            name: "roborev",
            status: "in_progress",
            conclusion: "",
            url: "",
            app: "roborev",
          }),
        ]),
        detailLoaded: true,
        detailSyncing: false,
      },
    });

    await fireEvent.click(
      screen.getByRole("button", {
        name: /CI: \d+ (passed|pending|failed|skipped) checks?/i,
      }),
    );

    expect(screen.getByText("build")).toBeTruthy();
    expect(screen.getByText("test")).toBeTruthy();
    expect(screen.getByText("lint")).toBeTruthy();
    expect(document.querySelectorAll(".ci-name")).toHaveLength(4);
    expect(document.querySelectorAll(".ci-row")).toHaveLength(4);
    expect(document.querySelectorAll("a.ci-row")).toHaveLength(3);
    expect(document.querySelector(".ci-row--static")).toBeTruthy();
    expect(screen.getByText("2m 15s")).toBeTruthy();
  });

  it("renders the mixed-state token cluster", () => {
    const checks = [
      mkCheck({ name: "f", conclusion: "failure" }),
      mkCheck({ name: "p1" }),
      mkCheck({ name: "p2" }),
      mkCheck({ name: "s", conclusion: "skipped" }),
      mkCheck({ name: "pend", status: "in_progress", conclusion: "" }),
    ];
    render(CIStatus, {
      props: {
        ...chipBaseProps,
        status: "failure",
        checksJSON: JSON.stringify(checks),
      },
    });
    expect(
      document.querySelector("[data-testid='ci-token-failed']")!.textContent,
    ).toContain("1");
    expect(
      document.querySelector("[data-testid='ci-token-pending']")!.textContent,
    ).toContain("1");
    expect(
      document.querySelector("[data-testid='ci-token-passed']")!.textContent,
    ).toContain("2");
    expect(
      document.querySelector("[data-testid='ci-token-skipped']")!.textContent,
    ).toContain("1");
  });

  it("hides the chip when there are zero checks and CIStatus is empty", () => {
    const { container } = render(CIStatus, {
      props: { ...chipBaseProps, checksJSON: "" },
    });
    expect(container.querySelector(".chip")).toBeNull();
  });

  it("hides the chip when CIStatus is set but CIChecksJSON is empty", () => {
    const { container } = render(CIStatus, {
      props: { ...chipBaseProps, status: "success", checksJSON: "" },
    });
    expect(container.querySelector(".chip")).toBeNull();
  });

  it("renders CI: unavailable chip when CIChecksJSON is malformed without leaking the raw payload to UI surfaces", () => {
    const sentinel = "supersecret_sentinel_xyz";
    const spy = vi.spyOn(console, "warn").mockImplementation(() => {});
    render(CIStatus, {
      props: { ...chipBaseProps, checksJSON: `{"x":"${sentinel}",` },
    });
    expect(screen.getByText(/CI:\s*unavailable/i)).toBeTruthy();
    const unavail = document.querySelector("[aria-disabled='true']");
    expect(unavail).not.toBeNull();
    const title = unavail!.getAttribute("title") ?? "";
    expect(title).toMatch(/CI unavailable:/i);
    expect(title).not.toContain(sentinel);
    const popover = document.querySelector("[data-testid='ci-unavailable-popover']");
    expect(popover).not.toBeNull();
    expect(popover!.textContent ?? "").not.toContain(sentinel);
    expect(popover!.textContent ?? "").toMatch(/Malformed JSON/);
    // aria-label on the chip element must also be sanitised.
    expect(unavail!.getAttribute("aria-label") ?? "").not.toContain(sentinel);
    spy.mockRestore();
  });

  it("fires malformed warning at most once per payload via console.warn", async () => {
    const spy = vi.spyOn(console, "warn").mockImplementation(() => {});
    const { rerender } = render(CIStatus, {
      props: { ...chipBaseProps, checksJSON: "{not json" },
    });
    await rerender({ ...chipBaseProps, checksJSON: "{not json" });
    expect(
      spy.mock.calls.filter(
        (c) => typeof c[0] === "string" && c[0].includes("Malformed"),
      ),
    ).toHaveLength(1);
    spy.mockRestore();
  });

  it("fires unknown warning at most once per distinct conclusion via console.warn", async () => {
    const spy = vi.spyOn(console, "warn").mockImplementation(() => {});
    const checks = JSON.stringify([{ status: "completed", conclusion: "weird_state" }]);
    const { rerender } = render(CIStatus, {
      props: { ...chipBaseProps, prKey: "A", checksJSON: checks },
    });
    await rerender({ ...chipBaseProps, prKey: "B", checksJSON: checks });
    expect(
      spy.mock.calls.filter(
        (c) =>
          typeof c[0] === "string" &&
          c[0].includes("Unrecognised CI conclusion"),
      ),
    ).toHaveLength(1);
    spy.mockRestore();
  });

  it("renders a single Unknown token when all checks have unrecognised conclusions", () => {
    const spy = vi.spyOn(console, "warn").mockImplementation(() => {});
    render(CIStatus, {
      props: {
        ...chipBaseProps,
        checksJSON: JSON.stringify([
          mkCheck({ name: "weird-only", conclusion: "mystery_state" }),
        ]),
      },
    });
    expect(document.querySelector("[data-testid='ci-token-unknown']")).not.toBeNull();
    expect(document.querySelector("[data-testid='ci-token-failed']")).toBeNull();
    expect(document.querySelector("[data-testid='ci-token-pending']")).toBeNull();
    expect(document.querySelector("[data-testid='ci-token-passed']")).toBeNull();
    expect(document.querySelector("[data-testid='ci-token-skipped']")).toBeNull();
    expect(
      spy.mock.calls.filter(
        (c) =>
          typeof c[0] === "string" &&
          c[0].includes("Unrecognised CI conclusion"),
      ),
    ).toHaveLength(1);
    spy.mockRestore();
  });

  // --- Dropdown structure tests (Task 11) ---

  it("dropdown shows summary header with check count and longest duration", () => {
    render(CIStatus, {
      props: {
        ...chipBaseProps,
        expanded: true,
        checksJSON: JSON.stringify([
          mkCheck({ duration_seconds: 30 }),
          mkCheck({ duration_seconds: 90 }),
        ]),
      },
    });
    expect(document.querySelector(".ci-summary")!.textContent).toMatch(
      /2 checks · longest 1m 30s/,
    );
  });

  it("dropdown renders five sections in fixed order when all non-zero", () => {
    const c = (conclusion: string, status = "completed") =>
      mkCheck({ status, conclusion });
    render(CIStatus, {
      props: {
        ...chipBaseProps,
        expanded: true,
        checksJSON: JSON.stringify([
          c("failure"),
          c("", "in_progress"),
          c("weird_new_state"),
          c("success"),
          c("skipped"),
        ]),
      },
    });
    const headings = Array.from(
      document.querySelectorAll(".ci-section-heading"),
    ).map((h) => h.textContent?.trim() ?? "");
    expect(headings).toEqual([
      "Failed (1)",
      "Pending (1)",
      "Unknown (1)",
      "Passed (1)",
      "Skipped (1)",
    ]);
  });

  it("dropdown hides zero-count sections", () => {
    render(CIStatus, {
      props: {
        ...chipBaseProps,
        expanded: true,
        checksJSON: JSON.stringify([mkCheck()]),
      },
    });
    expect(document.querySelector("[data-testid='ci-section-failed']")).toBeNull();
    expect(document.querySelector("[data-testid='ci-section-pending']")).toBeNull();
    expect(document.querySelector("[data-testid='ci-section-unknown']")).toBeNull();
    expect(document.querySelector("[data-testid='ci-section-skipped']")).toBeNull();
    expect(document.querySelector("[data-testid='ci-section-passed']")).not.toBeNull();
  });

  it("Passed section shows first 8 + Show 1 more toggle when count > 8", async () => {
    const checks = Array.from({ length: 9 }, (_, i) =>
      mkCheck({ name: `p${i}` }),
    );
    render(CIStatus, {
      props: {
        ...chipBaseProps,
        expanded: true,
        checksJSON: JSON.stringify(checks),
      },
    });
    expect(document.querySelectorAll(".ci-row")).toHaveLength(8);
    const toggle = screen.getByRole("button", { name: /Show 1 more passed/i });
    await fireEvent.click(toggle);
    expect(document.querySelectorAll(".ci-row")).toHaveLength(9);
    const collapseToggle = screen.getByRole("button", {
      name: /Show fewer passed/i,
    });
    await fireEvent.click(collapseToggle);
    expect(document.querySelectorAll(".ci-row")).toHaveLength(8);
  });

  it("dropdown row uses bucket Lucide icon (not ASCII glyph)", () => {
    render(CIStatus, {
      props: {
        ...chipBaseProps,
        expanded: true,
        checksJSON: JSON.stringify([mkCheck({ conclusion: "failure" })]),
      },
    });
    const row = document.querySelector(".ci-row")!;
    expect(row.querySelector("svg")).not.toBeNull();
    expect(row.textContent).not.toContain("✗");
  });

  it("expansion state resets when prKey changes", async () => {
    const checks = Array.from({ length: 9 }, (_, i) =>
      mkCheck({ name: `p${i}` }),
    );
    const { rerender } = render(CIStatus, {
      props: {
        ...chipBaseProps,
        prKey: "ext-A",
        expanded: true,
        checksJSON: JSON.stringify(checks),
      },
    });
    await fireEvent.click(
      screen.getByRole("button", { name: /Show 1 more passed/i }),
    );
    expect(document.querySelectorAll(".ci-row")).toHaveLength(9);
    await rerender({
      ...chipBaseProps,
      prKey: "ext-B",
      expanded: true,
      checksJSON: JSON.stringify(checks),
    });
    expect(document.querySelectorAll(".ci-row")).toHaveLength(8);
  });

  it("renders static circle for pending row under prefers-reduced-motion", () => {
    vi.spyOn(window, "matchMedia").mockReturnValue({
      matches: true,
    } as MediaQueryList);
    render(CIStatus, {
      props: {
        ...chipBaseProps,
        expanded: true,
        checksJSON: JSON.stringify([
          mkCheck({ status: "in_progress", conclusion: "" }),
        ]),
      },
    });
    const pendingSection = document.querySelector(
      "[data-testid='ci-section-pending']",
    )!;
    expect(pendingSection.querySelector(".spin")).toBeNull();
    expect(pendingSection.querySelector(".ci-row svg")).not.toBeNull();
  });
});
