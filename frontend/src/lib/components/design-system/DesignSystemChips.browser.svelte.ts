// Browser-tier migration of frontend/tests/e2e/design-system.spec.ts
// ("design system page renders chip matrix with shared styles"). That spec
// drove the standalone /design-system page only to read getComputedStyle off
// the shared Chip primitive. The chip matrix has no backend or app-shell
// dependency, so it is migrated here by mounting the real kit-ui Chip
// directly in a Chromium page and asserting the computed geometry/typography
// of kit-ui's strict size ladder (xs=10px/16px, sm=11px/18px). jsdom returns
// empty strings for these computed values, which is exactly why this belongs
// at the browser tier.

import { describe, expect, it } from "vite-plus/test";
import { page } from "vite-plus/test/browser";
import { render } from "vitest-browser-svelte";

// app.css imports the kit-ui theme tokens the chip tones resolve against.
// A real page needs no jsdom shims.
import "../../../app.css";

import DesignSystemChipsHarness from "./DesignSystemChipsHarness.svelte";

const TRANSPARENT = "rgba(0, 0, 0, 0)";

describe("design system chip matrix (browser)", () => {
  it("renders shared chip geometry and tokens with real computed styles", async () => {
    const { container } = render(DesignSystemChipsHarness);

    const assert = expect;

    const xsGreen = container.querySelector('[data-size="xs"] .kit-chip--tone-success');
    const smGreen = container.querySelector('[data-size="sm"] .kit-chip--tone-success');
    const muted = container.querySelector(".kit-chip--tone-muted");
    assert(xsGreen).not.toBeNull();
    assert(smGreen).not.toBeNull();
    assert(muted).not.toBeNull();

    // xs chip: kit-ui size modifier + --font-size-2xs token.
    const xsStyle = getComputedStyle(xsGreen as Element);
    assert(xsStyle.minHeight).toBe("16px");
    assert(xsStyle.fontSize).toBe("10px");
    assert(`${xsStyle.paddingLeft}/${xsStyle.paddingRight}`).toBe("6px/6px");

    // sm chip: 18px / --font-size-xs / 6px padding, painted background,
    // uppercase casing default.
    const smStyle = getComputedStyle(smGreen as Element);
    assert(smStyle.minHeight).toBe("18px");
    assert(smStyle.fontSize).toBe("11px");
    assert(`${smStyle.paddingLeft}/${smStyle.paddingRight}`).toBe("6px/6px");
    assert(smStyle.backgroundColor).not.toBe(TRANSPARENT);
    assert(smStyle.textTransform).toBe("uppercase");

    // Muted tone resolves to a real (non-transparent) inset background.
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

    // The descender chip (xs, plain-case) keeps the shared 16px chip box,
    // the geometry the e2e guarded against clipping.
    const descenderLabel = container.querySelector('[data-testid="descender-chip"]');
    assert(descenderLabel).not.toBeNull();
    const chip = (descenderLabel as Element).closest(".kit-chip");
    assert(chip).not.toBeNull();
    assert(Math.round((chip as Element).getBoundingClientRect().height)).toBe(16);
  });
});
