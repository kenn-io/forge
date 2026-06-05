import { describe, expect, it } from "vite-plus/test";

import { floatingPopoverStyle } from "./floatingPosition.js";

function rect(input: Partial<Pick<DOMRect, "left" | "right" | "top" | "bottom">>): DOMRect {
  return {
    x: 0,
    y: 0,
    width: 0,
    height: 0,
    top: input.top ?? 0,
    left: input.left ?? 0,
    right: input.right ?? input.left ?? 0,
    bottom: input.bottom ?? input.top ?? 0,
    toJSON: () => ({}),
  };
}

describe("floatingPopoverStyle", () => {
  it("right-aligns constrained popovers to the trigger when there is room", () => {
    const style = floatingPopoverStyle({
      trigger: rect({ right: 900, bottom: 100 }),
      viewportWidth: 1200,
      align: "end",
      edgeGap: 12,
      maxWidth: 360,
      constrainWidth: true,
    });

    expect(style).toContain("left: 540px");
    expect(style).toContain("width: 360px");
  });

  it("clamps constrained popovers to the viewport edge", () => {
    const style = floatingPopoverStyle({
      trigger: rect({ right: 1250, bottom: 100 }),
      viewportWidth: 1200,
      align: "end",
      edgeGap: 12,
      maxWidth: 360,
      constrainWidth: true,
    });

    expect(style).toContain("left: 828px");
  });

  it("uses the available viewport width when constrained popovers are narrow", () => {
    const style = floatingPopoverStyle({
      trigger: rect({ right: 260, bottom: 100 }),
      viewportWidth: 300,
      align: "end",
      edgeGap: 12,
      maxWidth: 360,
      constrainWidth: true,
    });

    expect(style).toContain("left: 12px");
    expect(style).toContain("width: 276px");
  });

  it("keeps measured start-aligned dropdowns inside the viewport", () => {
    const style = floatingPopoverStyle({
      trigger: rect({ left: 1180, bottom: 100 }),
      viewportWidth: 1200,
      popoverWidth: 200,
      align: "start",
    });

    expect(style).toContain("left: 992px");
    expect(style).not.toContain("width:");
  });

  it("places measured dropdowns above the trigger when they would overflow below", () => {
    const style = floatingPopoverStyle({
      trigger: rect({
        left: 50,
        right: 150,
        top: 760,
        bottom: 780,
      }),
      viewportWidth: 1000,
      viewportHeight: 800,
      popoverWidth: 200,
      popoverHeight: 100,
    });

    expect(style).toContain("top: 656px");
  });
});
