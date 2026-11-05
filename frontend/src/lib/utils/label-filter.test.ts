import { describe, expect, it } from "vite-plus/test";

import {
  cycleLabelFilter,
  labelFilterChoices,
  labelFilterSummary,
  matchesLabelFilters,
  type LabelFilters,
} from "./label-filter.js";

const labels = (...names: string[]) => names.map((name) => ({ name, color: "d73a4a" }));

describe("label filters", () => {
  it("cycles a label from neutral to required to excluded and back to neutral", () => {
    let filters: LabelFilters = new Map();
    filters = cycleLabelFilter(filters, "bug");
    expect(filters.get("bug")?.mode).toBe("require");
    filters = cycleLabelFilter(filters, "bug");
    expect(filters.get("bug")?.mode).toBe("exclude");
    filters = cycleLabelFilter(filters, "bug");
    expect(filters.size).toBe(0);
  });

  it("requires every required label and rejects any excluded label", () => {
    let filters: LabelFilters = new Map();
    filters = cycleLabelFilter(filters, "bug");
    filters = cycleLabelFilter(filters, "ui");
    filters = cycleLabelFilter(filters, "wontfix");
    filters = cycleLabelFilter(filters, "wontfix");

    expect(matchesLabelFilters(labels("bug", "ui"), filters)).toBe(true);
    expect(matchesLabelFilters(labels("bug"), filters)).toBe(false);
    expect(matchesLabelFilters(labels("bug", "ui", "wontfix"), filters)).toBe(false);
    expect(matchesLabelFilters(undefined, filters)).toBe(false);
  });

  it("matches label names across repositories regardless of case", () => {
    const filters = cycleLabelFilter(new Map(), "Bug");
    expect(matchesLabelFilters(labels("bug"), filters)).toBe(true);
    expect(matchesLabelFilters(labels("BUG"), cycleLabelFilter(filters, "bug"))).toBe(false);
  });

  it("lets an exclude-only filter keep unlabeled items", () => {
    const filters = cycleLabelFilter(cycleLabelFilter(new Map(), "wontfix"), "wontfix");
    expect(matchesLabelFilters(undefined, filters)).toBe(true);
    expect(matchesLabelFilters(labels("wontfix"), filters)).toBe(false);
  });

  it("orders choices by loaded count and keeps a selected label no item carries", () => {
    const filters = cycleLabelFilter(new Map(), "stale");
    const choices = labelFilterChoices(
      [{ labels: labels("docs", "bug") }, { labels: labels("Bug") }, { labels: undefined }],
      filters,
    );
    expect(choices.map((choice) => [choice.name, choice.count, choice.mode])).toEqual([
      ["bug", 2, undefined],
      ["docs", 1, undefined],
      ["stale", 0, "require"],
    ]);
  });

  it("summarizes the selection for the Labels row", () => {
    let filters: LabelFilters = new Map();
    expect(labelFilterSummary(filters)).toBe("Any");
    filters = cycleLabelFilter(filters, "bug");
    filters = cycleLabelFilter(filters, "ui");
    expect(labelFilterSummary(filters)).toBe("2 req");
    filters = cycleLabelFilter(filters, "ui");
    expect(labelFilterSummary(filters)).toBe("1 req, 1 excl");
    filters = cycleLabelFilter(filters, "bug");
    filters = cycleLabelFilter(filters, "bug");
    expect(labelFilterSummary(filters)).toBe("1 excl");
  });
});
