import { cleanup, fireEvent, render } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import ScrollBoxTestHost from "./ScrollBoxTestHost.svelte";

let restoreHeights: (() => void) | undefined;

// jsdom has no layout and the shared test setup stubs ResizeObserver as a
// no-op, so the viewport/content heights ScrollBox measures on mount are
// faked at the prototype level before render.
function stubHeights(contentHeight: number): void {
  const clientDesc = Object.getOwnPropertyDescriptor(Element.prototype, "clientHeight");
  const offsetDesc = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "offsetHeight");
  Object.defineProperty(Element.prototype, "clientHeight", {
    configurable: true,
    get(this: Element) {
      return this.classList.contains("scroll-box__viewport") ? 200 : 0;
    },
  });
  Object.defineProperty(HTMLElement.prototype, "offsetHeight", {
    configurable: true,
    get(this: HTMLElement) {
      return this.classList.contains("scroll-box__content") ? contentHeight : 0;
    },
  });
  restoreHeights = () => {
    if (clientDesc) Object.defineProperty(Element.prototype, "clientHeight", clientDesc);
    if (offsetDesc) Object.defineProperty(HTMLElement.prototype, "offsetHeight", offsetDesc);
  };
}

afterEach(() => {
  cleanup();
  restoreHeights?.();
  restoreHeights = undefined;
  vi.useRealTimers();
});

function getViewport(): HTMLDivElement {
  return document.querySelector(".scroll-box__viewport") as HTMLDivElement;
}

describe("ScrollBox", () => {
  it("shows the thumb while scrolling and hides it after the timeout", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    stubHeights(800);
    render(ScrollBoxTestHost);

    const viewport = getViewport();
    expect(document.querySelector(".scroll-box__indicator.visible")).toBeNull();

    viewport.scrollTop = 120;
    await fireEvent.scroll(viewport);
    expect(document.querySelector(".scroll-box__indicator.visible")).not.toBeNull();

    await vi.advanceTimersByTimeAsync(700);
    expect(document.querySelector(".scroll-box__indicator.visible")).toBeNull();
  });

  it("keeps the thumb hidden when content fits the viewport", async () => {
    stubHeights(150);
    render(ScrollBoxTestHost);

    const viewport = getViewport();
    await fireEvent.scroll(viewport);
    expect(document.querySelector(".scroll-box__indicator.visible")).toBeNull();
  });

  it("forwards scroll events to the onscroll prop", async () => {
    stubHeights(800);
    const onscroll = vi.fn();
    render(ScrollBoxTestHost, { props: { onscroll } });

    await fireEvent.scroll(getViewport());
    expect(onscroll).toHaveBeenCalledTimes(1);
    expect(onscroll.mock.calls[0][0]).toBeInstanceOf(Event);
  });

  it("exposes the scrolling element through the bindable viewport", () => {
    stubHeights(800);
    const seen: Array<HTMLDivElement | undefined> = [];
    render(ScrollBoxTestHost, { props: { onviewport: (el) => seen.push(el) } });

    const bound = seen.at(-1);
    expect(bound).toBeInstanceOf(HTMLDivElement);
    expect(bound?.classList.contains("scroll-box__viewport")).toBe(true);
  });

  it("labels the scroll region for keyboard users", () => {
    stubHeights(800);
    render(ScrollBoxTestHost);

    const viewport = getViewport();
    expect(viewport.getAttribute("role")).toBe("region");
    expect(viewport.getAttribute("aria-label")).toBe("Test scroll region");
    expect(viewport.getAttribute("tabindex")).toBe("0");
  });
});
