import { expect, it } from "vitest";
import { actionsPaneBounds, fitPaneWidths, minRunsPaneWidth, resizeHandleWidth } from "./actions-pane-widths.js";

const requested = { rail: 200, catalog: 230, dispatch: 300 };
const all = ["rail", "catalog", "dispatch"] as const;

it("leaves requested widths alone when the runs pane has room", () => {
  expect(fitPaneWidths(requested, 1600, all)).toEqual(requested);
});

it("gives way from the pane nearest the runs table first, never below a pane minimum", () => {
  const used = 200 + 230 + 300 + 3 * resizeHandleWidth;
  expect(fitPaneWidths(requested, used + minRunsPaneWidth - 40, all)).toEqual({
    rail: 200,
    catalog: 230,
    dispatch: 260,
  });

  expect(fitPaneWidths(requested, 600, all)).toEqual({
    rail: actionsPaneBounds.rail.min,
    catalog: actionsPaneBounds.catalog.min,
    dispatch: actionsPaneBounds.dispatch.min,
  });
});

it("ignores a hidden repository rail when budgeting space", () => {
  const used = 230 + 300 + 2 * resizeHandleWidth;
  expect(fitPaneWidths(requested, used + minRunsPaneWidth, ["catalog", "dispatch"])).toEqual(requested);
});
