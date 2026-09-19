import { expect, it } from "vite-plus/test";
import { createRecentDetails } from "./recent-details.js";

it("bounds retained detail snapshots and keeps a revisited item", () => {
  const recent = createRecentDetails<number>();
  for (let number = 0; number < 10; number++) recent.remember(String(number), number);
  recent.remember("0", 100);
  recent.remember("10", 10);
  expect(recent.get("1")).toBeUndefined();
  expect(recent.get("0")).toBe(100);
  expect(recent.get("10")).toBe(10);
});
