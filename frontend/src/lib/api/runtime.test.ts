import { describe, expect, it } from "vite-plus/test";

import { orvalQueryString } from "./runtime.js";

describe("runtime", () => {
  it("serializes array query parameters as repeated keys", () => {
    const query = orvalQueryString({ types: ["comment", "review"] });

    expect(query).toBe("types=comment&types=review");
  });

  it("omits an optional query string when no parameters are supplied", () => {
    expect(orvalQueryString()).toBe("");
  });
});
