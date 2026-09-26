import type { Attachment } from "svelte/attachments";

/** Rows mounted before the user scrolls; comfortably more than one screen. */
export const INITIAL_RENDER_BUDGET = 100;
/** Rows added each time the user scrolls near the end of the mounted rows. */
export const RENDER_BUDGET_STEP = 200;

/**
 * Returns the number of rows to mount: the scroll-driven budget, extended so
 * the selected row (and a little context after it) is always mounted.
 */
export function effectiveRenderBudget(budget: number, selectedIndex: number): number {
  return selectedIndex < 0 ? budget : Math.max(budget, selectedIndex + 1 + RENDER_BUDGET_STEP / 4);
}

export interface BudgetedGroup<T> {
  items: T[];
  collapsed: boolean;
}

/**
 * Slices grouped rows so that at most `budget` rows are mounted in display
 * order. Collapsed groups mount no rows and so consume none of the budget;
 * groups after the budget runs out are dropped entirely.
 */
export function applyGroupRenderBudget<G extends BudgetedGroup<T>, T>(
  groups: readonly G[],
  budget: number,
): { groups: G[]; truncated: boolean } {
  const out: G[] = [];
  let remaining = budget;
  for (const group of groups) {
    if (group.collapsed) {
      out.push(group);
      continue;
    }
    if (remaining <= 0) return { groups: out, truncated: true };
    if (group.items.length <= remaining) {
      out.push(group);
      remaining -= group.items.length;
      continue;
    }
    out.push({ ...group, items: group.items.slice(0, remaining) });
    return { groups: out, truncated: true };
  }
  return { groups: out, truncated: false };
}

function scrollParent(node: Element): Element | null {
  for (let el = node.parentElement; el !== null; el = el.parentElement) {
    const overflowY = getComputedStyle(el).overflowY;
    if (overflowY === "auto" || overflowY === "scroll") return el;
  }
  return null;
}

/**
 * Attach to a sentinel placed after the mounted rows. Calls `onReveal` when
 * the sentinel comes within `margin` of the scroll container's visible area.
 * Re-create the attachment whenever the budget grows (read the budget inside
 * the attachment factory) so a sentinel that stays visible fires again.
 */
export function revealWhenNear(onReveal: () => void, margin = "800px"): Attachment<Element> {
  return (node) => {
    if (typeof IntersectionObserver === "undefined") return;
    const observer = new IntersectionObserver(
      (entries) => {
        if (entries.some((entry) => entry.isIntersecting)) onReveal();
      },
      { root: scrollParent(node), rootMargin: `0px 0px ${margin} 0px` },
    );
    observer.observe(node);
    return () => observer.disconnect();
  };
}
