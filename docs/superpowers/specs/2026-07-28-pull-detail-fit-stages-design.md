# Pull Detail Action Fit Stages

## Problem

PullDetail hand-selects compact Button internals with container-query CSS.
That override replaces Kit UI's flex-centered short-label wrapper with a
blockified inline wrapper, shifting action text upward. It also uses a
separate breakpoint to hide only the Close icon.

## Design

Use Kit UI `FitStages`, the adaptive action-row primitive, to choose the
richest action rendering that fits the pull-detail pane:

1. full Button labels and icons;
2. short Button labels and icons.

Each stage passes its chosen text through Button's normal `label` prop, so
Kit UI retains ownership of label layout and vertical alignment. The existing
340px Actions-menu fallback remains the final narrow state. PullDetail removes
all CSS that reaches into Button label or icon internals.

The selected primary-action stage also chooses the full or short label for the
separate workspace action row.

## Verification

Exercise a real PullDetail at wide and medium container widths. Verify that
`FitStages` selects the fitting label set, every visible action uses Kit UI's
normal centered label wrapper, and icons remain visible. Retain the existing
narrow Actions-menu coverage.
