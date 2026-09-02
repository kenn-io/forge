import { describe, expect, it } from "vite-plus/test";
import {
  carryMobileListOrigin,
  mobileListBackLabel,
  mobileListOriginState,
  mobileListRoute,
  readMobileListBackDepth,
  readMobileListOrigin,
  rememberMobileListPosition,
  takeMobileListPosition,
} from "./mobile-list-return.js";

describe("mobile list return", () => {
  it("round-trips the originating list through history state", () => {
    expect(readMobileListOrigin(mobileListOriginState("pulls"))).toBe("pulls");
    expect(readMobileListOrigin(mobileListOriginState("activity"))).toBe("activity");
    expect(readMobileListOrigin(null)).toBeUndefined();
    expect(readMobileListOrigin({ kennForgeMobileListOrigin: "terminal" })).toBeUndefined();
  });

  it("maps each origin to its phone list route and back label", () => {
    expect(mobileListRoute("pulls")).toBe("/m/pulls");
    expect(mobileListRoute("issues")).toBe("/m/issues");
    expect(mobileListRoute("activity")).toBe("/m");
    expect(mobileListBackLabel("pulls")).toBe("Pull requests");
    expect(mobileListBackLabel("issues")).toBe("Issues");
    expect(mobileListBackLabel("activity")).toBe("Activity");
  });

  it("hands a parked list position back exactly once", () => {
    rememberMobileListPosition("mrs:all", { scrollTop: 640, pageLimit: 90 });
    expect(takeMobileListPosition("mrs:all")).toEqual({ scrollTop: 640, pageLimit: 90 });
    expect(takeMobileListPosition("mrs:all")).toBeUndefined();
    expect(takeMobileListPosition("issues:all")).toBeUndefined();
  });
});

describe("carryMobileListOrigin", () => {
  it("copies the list origin onto the next entry and counts pushed entries", () => {
    const pushed = carryMobileListOrigin({ ...mobileListOriginState("activity"), other: 1 }, "push");
    expect(pushed).toEqual(mobileListOriginState("activity", 2));
    expect(readMobileListBackDepth(pushed)).toBe(2);
    expect(carryMobileListOrigin(pushed, "replace")).toEqual(mobileListOriginState("activity", 2));
    expect(carryMobileListOrigin(null, "push")).toBeUndefined();
    expect(carryMobileListOrigin({ other: 1 }, "push")).toBeUndefined();
  });

  it("treats a missing or malformed depth as one step", () => {
    expect(readMobileListBackDepth(null)).toBe(1);
    expect(readMobileListBackDepth({ kennForgeMobileListOrigin: "pulls" })).toBe(1);
    expect(readMobileListBackDepth({ kennForgeMobileListBackDepth: 0 })).toBe(1);
  });
});
