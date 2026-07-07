import { beforeEach, describe, expect, test } from "vite-plus/test";

import { clearInteraction, markInteractionStart, measureInteraction } from "./interactionTiming.js";

describe("interaction timing", () => {
  beforeEach(() => {
    performance.clearMarks();
    performance.clearMeasures();
  });

  test("measures a phase against the interaction start mark", () => {
    markInteractionStart("test:interaction", "1");

    measureInteraction("test:interaction", "phase-one", "1");
    measureInteraction("test:interaction", "phase-two", "1");

    expect(performance.getEntriesByName("test:interaction:phase-one", "measure")).toHaveLength(1);
    expect(performance.getEntriesByName("test:interaction:phase-two", "measure")).toHaveLength(1);
  });

  test("a phase without a start mark records nothing", () => {
    measureInteraction("test:interaction", "phase-one", "missing");

    expect(performance.getEntriesByName("test:interaction:phase-one", "measure")).toHaveLength(0);
  });

  test("clearing an interaction drops its start mark", () => {
    markInteractionStart("test:interaction", "1");
    clearInteraction("test:interaction", "1");

    measureInteraction("test:interaction", "phase-one", "1");

    expect(performance.getEntriesByName("test:interaction:phase-one", "measure")).toHaveLength(0);
  });

  test("tokens keep concurrent interactions separate", () => {
    markInteractionStart("test:interaction", "1");
    markInteractionStart("test:interaction", "2");
    clearInteraction("test:interaction", "1");

    measureInteraction("test:interaction", "phase-one", "1");
    measureInteraction("test:interaction", "phase-one", "2");

    expect(performance.getEntriesByName("test:interaction:phase-one", "measure")).toHaveLength(1);
  });
});
