import { cleanup, render } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import CITokenCluster from "./CITokenCluster.svelte";
import { composeAriaLabel } from "./CITokenCluster.svelte";
import type { CIBucketedChecks } from "../../utils/ci-buckets.js";

function bucketed(
  counts: Partial<Record<"failed" | "pending" | "passed" | "skipped" | "unknown", number>>,
): CIBucketedChecks {
  const make = (n: number) =>
    Array.from({ length: n }, () => ({
      name: "",
      status: "completed",
      conclusion: "",
      url: "",
      app: "",
    }));
  const failed = make(counts.failed ?? 0);
  const pending = make(counts.pending ?? 0);
  const passed = make(counts.passed ?? 0);
  const skipped = make(counts.skipped ?? 0);
  const unknown = make(counts.unknown ?? 0);
  return {
    failed,
    pending,
    passed,
    skipped,
    unknown,
    all: [...failed, ...pending, ...unknown, ...passed, ...skipped],
    longestCompletedDurationSeconds: undefined,
  };
}

describe("CITokenCluster", () => {
  afterEach(() => cleanup());

  it("renders only non-zero tokens in fixed severity order", () => {
    render(CITokenCluster, {
      props: {
        bucketed: bucketed({ failed: 1, passed: 23, skipped: 2 }),
        size: "default",
      },
    });
    const tokens = document.querySelectorAll("[data-testid^='ci-token-']");
    expect(tokens.length).toBe(3);
    expect(tokens[0].getAttribute("data-testid")).toBe("ci-token-failed");
    expect(tokens[1].getAttribute("data-testid")).toBe("ci-token-passed");
    expect(tokens[2].getAttribute("data-testid")).toBe("ci-token-skipped");
  });

  it("emits the unknown token between pending and passed when present", () => {
    render(CITokenCluster, {
      props: {
        bucketed: bucketed({
          failed: 1,
          pending: 2,
          unknown: 3,
          passed: 4,
          skipped: 5,
        }),
        size: "default",
      },
    });
    const tokens = document.querySelectorAll("[data-testid^='ci-token-']");
    const ids = Array.from(tokens).map((t) => t.getAttribute("data-testid"));
    expect(ids).toEqual([
      "ci-token-failed",
      "ci-token-pending",
      "ci-token-unknown",
      "ci-token-passed",
      "ci-token-skipped",
    ]);
  });

  it("renders nothing when all buckets are empty", () => {
    render(CITokenCluster, {
      props: { bucketed: bucketed({}), size: "default" },
    });
    expect(document.querySelectorAll("[data-testid^='ci-token-']").length).toBe(0);
  });

  it("token children are aria-hidden", () => {
    render(CITokenCluster, {
      props: { bucketed: bucketed({ failed: 1 }), size: "default" },
    });
    const token = document.querySelector("[data-testid='ci-token-failed']")!;
    expect(token.getAttribute("aria-hidden")).toBe("true");
  });

  it("pending token has the spin class when prefers-reduced-motion: reduce is OFF (animated chip path)", () => {
    // matchMedia stub returning matches=false ↔ reduced-motion preference is OFF.
    const mqlOff = {
      matches: false,
      media: "(prefers-reduced-motion: reduce)",
      addEventListener: () => {},
      removeEventListener: () => {},
    };
    vi.stubGlobal("matchMedia", vi.fn().mockReturnValue(mqlOff));
    render(CITokenCluster, {
      props: { bucketed: bucketed({ pending: 1 }), size: "default" },
    });
    const token = document.querySelector("[data-testid='ci-token-pending']")!;
    // The animated LoaderCircleIcon is wrapped in `.spin`. When reduced-motion
    // is OFF the cluster mounts the spinning variant.
    expect(token.querySelector(".spin")).not.toBeNull();
    vi.unstubAllGlobals();
  });

  it("pending token has no spin class when prefers-reduced-motion: reduce is ON (static chip path)", () => {
    const mqlOn = {
      matches: true,
      media: "(prefers-reduced-motion: reduce)",
      addEventListener: () => {},
      removeEventListener: () => {},
    };
    vi.stubGlobal("matchMedia", vi.fn().mockReturnValue(mqlOn));
    render(CITokenCluster, {
      props: { bucketed: bucketed({ pending: 1 }), size: "default" },
    });
    const token = document.querySelector("[data-testid='ci-token-pending']")!;
    // Reduced motion ON → static CircleIcon, no `.spin` wrapper.
    expect(token.querySelector(".spin")).toBeNull();
    vi.unstubAllGlobals();
  });

  it("pending token has no spin class when pendingStyle='static' (sidebar path) regardless of reduced-motion", () => {
    const mqlOff = {
      matches: false,
      media: "(prefers-reduced-motion: reduce)",
      addEventListener: () => {},
      removeEventListener: () => {},
    };
    vi.stubGlobal("matchMedia", vi.fn().mockReturnValue(mqlOff));
    render(CITokenCluster, {
      props: {
        bucketed: bucketed({ pending: 1 }),
        size: "compact",
        pendingStyle: "static",
      },
    });
    const token = document.querySelector("[data-testid='ci-token-pending']")!;
    expect(token.querySelector(".spin")).toBeNull();
    vi.unstubAllGlobals();
  });
});

describe("composeAriaLabel", () => {
  it("uses singular for 1, plural for others", () => {
    expect(composeAriaLabel(bucketed({ failed: 1, pending: 5 }))).toBe("CI: 1 failed check, 5 pending checks");
  });

  it("omits zero buckets", () => {
    expect(composeAriaLabel(bucketed({ passed: 3 }))).toBe("CI: 3 passed checks");
  });
});
