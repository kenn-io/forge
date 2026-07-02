/*
 * kit-ui's focus trap filters tabbables by offsetParent to skip hidden
 * subtrees. jsdom has no layout, so offsetParent is always null and the trap
 * would see zero tabbable elements. Tests that assert real Tab-wrap behavior
 * install this stub so every attached element reports its parent instead.
 *
 * Call from beforeAll/afterAll:
 *   beforeAll(installOffsetParentStub);
 *   afterAll(removeOffsetParentStub);
 */

export function installOffsetParentStub(): void {
  Object.defineProperty(HTMLElement.prototype, "offsetParent", {
    configurable: true,
    get(this: HTMLElement) {
      return this.parentElement;
    },
  });
}

export function removeOffsetParentStub(): void {
  delete (HTMLElement.prototype as unknown as Record<string, unknown>)["offsetParent"];
}
