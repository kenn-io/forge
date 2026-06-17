// Browser-tier migration of frontend/tests/e2e/design-system.spec.ts
// ("design system page renders chip matrix with shared styles"). That spec
// drove the standalone /design-system page only to read getComputedStyle off
// the shared Chip primitive. The chip matrix has no backend or app-shell
// dependency, so it is migrated here by mounting the real Chip directly in a
// Chromium page and asserting the identical computed geometry/typography the
// e2e pinned. jsdom returns empty strings for these computed values, which is
// exactly why this belongs at the browser tier.

import { describe, expect, it } from "vite-plus/test";
import { page } from "vite-plus/test/browser";
import { render } from "vitest-browser-svelte";

// app.css provides --font-size-2xs/--font-size-xs and the accent/bg tokens the
// chip color classes resolve against. A real page needs no jsdom shims.
import "../../../app.css";

import DesignSystemChipsHarness from "./DesignSystemChipsHarness.svelte";

const TRANSPARENT = "rgba(0, 0, 0, 0)";

describe("design system chip matrix (browser)", () => {
  it("renders shared chip geometry and tokens with real computed styles", async () => {
    const { container } = render(DesignSystemChipsHarness);

    const assert = expect;

    const smGreen = container.querySelector('[data-size="sm"] .chip--green');
    const mdGreen = container.querySelector('[data-size="md"] .chip--green');
    const muted = container.querySelector(".chip--muted");
    assert(smGreen).not.toBeNull();
    assert(mdGreen).not.toBeNull();
    assert(muted).not.toBeNull();

    // Small chip: shared size modifier from Chip.svelte + --font-size-2xs token.
    const smStyle = getComputedStyle(smGreen as Element);
    assert(smStyle.minHeight).toBe("18px");
    assert(smStyle.fontSize).toBe("10px");
    assert(`${smStyle.paddingLeft}/${smStyle.paddingRight}`).toBe("6px/6px");

    // Medium chip: 22px / --font-size-xs / 8px padding, painted background,
    // uppercase casing default.
    const mdStyle = getComputedStyle(mdGreen as Element);
    assert(mdStyle.minHeight).toBe("22px");
    assert(mdStyle.fontSize).toBe("11px");
    assert(`${mdStyle.paddingLeft}/${mdStyle.paddingRight}`).toBe("8px/8px");
    assert(mdStyle.backgroundColor).not.toBe(TRANSPARENT);
    assert(mdStyle.textTransform).toBe("uppercase");

    // Muted variant resolves to a real (non-transparent) inset background.
    assert(getComputedStyle(muted as Element).backgroundColor).not.toBe(TRANSPARENT);
  });

  it("honors plain-case opt-out and interactive cursor at the chip tier", async () => {
    const { container } = render(DesignSystemChipsHarness);

    const assert = expect;

    // uppercase={false} drops text-transform and letter-spacing.
    const plain = page.getByText("plain case", { exact: true }).element();
    const plainStyle = getComputedStyle(plain);
    assert(plainStyle.textTransform).toBe("none");
    assert(plainStyle.letterSpacing).toBe("normal");

    // Interactive chip renders as a real button with pointer cursor.
    const interactive = page.getByRole("button", { name: "Interactive" }).element();
    assert(interactive.tagName).toBe("BUTTON");
    assert(getComputedStyle(interactive).cursor).toBe("pointer");

    // The descender chip (small, plain-case) keeps the shared 18px chip box,
    // the geometry the e2e guarded against clipping.
    const descenderLabel = container.querySelector('[data-testid="descender-chip"]');
    assert(descenderLabel).not.toBeNull();
    const chip = (descenderLabel as Element).closest(".chip");
    assert(chip).not.toBeNull();
    assert(Math.round((chip as Element).getBoundingClientRect().height)).toBe(18);
  });
});
