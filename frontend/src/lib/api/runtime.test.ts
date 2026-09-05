import { describe, expect, it } from "vite-plus/test";

import { getListActivityUrl } from "./generated/activity/activity.js";
import { getGetRepoBrowserLastChangedUrl } from "./generated/repositories/repositories.js";

describe("runtime", () => {
  it("serializes array query parameters as comma-separated values for Huma", () => {
    const query = getListActivityUrl({ types: ["comment", "review"] });

    expect(query).toBe("/activity?types=comment%2Creview");
  });

  it("serializes repository browser paths as repeated keys", () => {
    const url = getGetRepoBrowserLastChangedUrl(
      { provider: "github", owner: "acme", name: "widgets" },
      { path: ["README.md", "docs/guide.md"] },
    );

    expect(url).toBe("/repo/github/acme/widgets/browser/last-changed?path=README.md&path=docs%2Fguide.md");
  });

  it("omits an optional query string when no parameters are supplied", () => {
    expect(getListActivityUrl()).toBe("/activity");
  });
});
