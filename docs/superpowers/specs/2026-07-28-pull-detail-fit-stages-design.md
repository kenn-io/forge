# Pull Detail Action Fit Stages

## Problem

PullDetail hand-selects compact Button internals with container-query CSS. The
override replaces Kit UI's flex-centered short-label wrapper with a blockified
inline wrapper, shifting action text upward. It also uses a separate breakpoint
to hide only the Close icon.

## Design

Use Kit UI `FitStages`, the adaptive control-rendering primitive, to select
between:

1. full action labels and icons;
2. compact action labels and icons.

Each stage passes its selected text through Button's normal `label` prop, so
Kit UI retains ownership of label layout and vertical alignment. PullDetail
does not style `.kit-button__label`, `.kit-button__short-label`, or Button
icons. The existing 340px Actions-menu fallback remains the final narrow
state.

`SegmentedControl` is not the applicable primitive: it is a mutually exclusive
radio selector, while these are independent commands. `FitStages` is the
measurement-driven control intended for renderings of the same controls at
decreasing widths.

## Verification

Exercise a real PullDetail through wide, compact, and narrow container widths.
Verify that `FitStages` selects the richest fitting action row, every visible
action uses Kit UI's normal centered label wrapper, icons remain visible, and
the existing narrow Actions-menu behavior is retained.
