import { autoReposition, floatingPopoverStyle } from "@kenn-io/kit-ui";

/** Keep workspace menus outside the scrolling tab strip and terminal layers. */
export function mountTerminalPopover(panel: HTMLElement, anchor: HTMLElement): () => void {
  // Nested controls must stay inside their owner's outside-click boundary.
  (anchor.closest(".controls-popover") ?? document.body).appendChild(panel);
  const position = () => {
    panel.style.cssText = floatingPopoverStyle({
      trigger: anchor.getBoundingClientRect(),
      viewportWidth: window.innerWidth,
      viewportHeight: window.innerHeight,
      popoverWidth: panel.offsetWidth,
      popoverHeight: panel.offsetHeight,
      align: "end",
    });
  };
  position();
  const stop = autoReposition(() => [anchor, panel], position);
  return () => {
    stop();
    panel.remove();
  };
}
