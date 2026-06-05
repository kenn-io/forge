type FloatingAlign = "start" | "end";

export interface FloatingPopoverInput {
  trigger: Pick<DOMRect, "left" | "right" | "top" | "bottom">;
  viewportWidth: number;
  viewportHeight?: number;
  popoverWidth?: number;
  popoverHeight?: number;
  align?: FloatingAlign;
  edgeGap?: number;
  triggerGap?: number;
  maxWidth?: number;
  constrainWidth?: boolean;
}

export function floatingPopoverStyle({
  trigger,
  viewportWidth,
  viewportHeight,
  popoverWidth,
  popoverHeight,
  align = "start",
  edgeGap = 8,
  triggerGap = 4,
  maxWidth,
  constrainWidth = false,
}: FloatingPopoverInput): string {
  const availableWidth = Math.max(0, viewportWidth - edgeGap * 2);
  const width = constrainWidth ? Math.min(maxWidth ?? availableWidth, availableWidth) : (popoverWidth ?? 0);
  const left = clamp(
    align === "end" ? trigger.right - width : trigger.left,
    edgeGap,
    Math.max(edgeGap, viewportWidth - width - edgeGap),
  );
  const top = floatingTop({
    trigger,
    popoverHeight,
    viewportHeight,
    edgeGap,
    triggerGap,
  });

  const style = [`left: ${Math.round(left)}px`, `top: ${Math.round(top)}px`];
  if (constrainWidth) {
    style.push(`width: ${Math.round(width)}px`);
  }
  return style.join("; ");
}

interface FloatingTopInput {
  trigger: Pick<DOMRect, "top" | "bottom">;
  popoverHeight: number | undefined;
  viewportHeight: number | undefined;
  edgeGap: number;
  triggerGap: number;
}

function floatingTop({ trigger, popoverHeight, viewportHeight, edgeGap, triggerGap }: FloatingTopInput): number {
  const below = trigger.bottom + triggerGap;
  if (popoverHeight === undefined || viewportHeight === undefined) {
    return below;
  }

  const above = trigger.top - popoverHeight - triggerGap;
  return below + popoverHeight > viewportHeight - edgeGap ? Math.max(edgeGap, above) : below;
}

function clamp(value: number, min: number, max: number): number {
  return Math.min(Math.max(min, value), max);
}
