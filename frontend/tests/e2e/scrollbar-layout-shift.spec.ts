import { expect, test, type Locator } from "@playwright/test";
import type { PullDetail } from "../../src/lib/api/types.js";
import { createMockApiHandler } from "../../src/test/mockApiFetch";
import { mockApi } from "./support/mockApi";

// Headless Chromium hides scrollbars by default, which would hide the
// scrollbar-width shift these tests guard against.
test.skip(({ browserName }) => browserName !== "chromium", "--hide-scrollbars is a Chromium launch argument");
test.use({
  launchOptions: { ignoreDefaultArgs: ["--hide-scrollbars"] },
  viewport: { width: 1700, height: 900 },
});

function scrollMetrics(scroller: Locator) {
  return scroller.evaluate((element: HTMLElement) => ({
    scrolls: element.scrollHeight > element.clientHeight,
    scrollbarWidth: element.offsetWidth - element.clientWidth,
  }));
}

test("pull request column stays put when an opened chip panel makes the conversation scroll", async ({ page }) => {
  await mockApi(page);
  const api = createMockApiHandler();
  await page.route("**/api/v1/pulls/github/acme/widgets/42", async (route) => {
    const response = api.handle({ method: "GET", url: new URL(route.request().url()), bodyText: "" });
    const detail = (await response.json()) as PullDetail;
    detail.merge_request.CIStatus = "success";
    detail.merge_request.CIChecksJSON = JSON.stringify(
      Array.from({ length: 30 }, (_, index) => ({
        name: `check-${index}`,
        status: "completed",
        conclusion: "success",
        url: "",
        app: "CI",
      })),
    );
    await route.fulfill({ json: detail });
  });

  await page.goto("/pulls/github/acme/widgets/42");
  const scroller = page.getByRole("region", { name: "Pull request conversation" });
  const heading = scroller.getByRole("heading", { level: 2 });
  await expect(heading).toBeVisible();
  expect((await scrollMetrics(scroller)).scrolls).toBe(false);
  const closedBox = await heading.boundingBox();

  await page.getByTestId("ci-chip").click();
  await expect(page.getByText("30 checks")).toBeVisible();

  const opened = await scrollMetrics(scroller);
  expect(opened.scrolls).toBe(true);
  expect(opened.scrollbarWidth).toBeGreaterThan(0);
  expect((await heading.boundingBox())?.x).toBe(closedBox?.x);
});

test("issue column stays put when collapsing the description stops the conversation scrolling", async ({ page }) => {
  await mockApi(page);
  const api = createMockApiHandler();
  await page.route("**/api/v1/issues/github/acme/widgets/7", async (route) => {
    const response = api.handle({ method: "GET", url: new URL(route.request().url()), bodyText: "" });
    const detail = (await response.json()) as { issue: { Body: string } };
    detail.issue.Body = Array.from({ length: 60 }, (_, index) => `Paragraph ${index} of a long description.`).join(
      "\n\n",
    );
    await route.fulfill({ json: detail });
  });

  await page.goto("/issues/github/acme/widgets/7");
  const scroller = page.getByRole("region", { name: "Issue conversation" });
  const heading = scroller.getByRole("heading", { level: 2 });
  await expect(heading).toBeVisible();
  const expanded = await scrollMetrics(scroller);
  expect(expanded.scrolls).toBe(true);
  expect(expanded.scrollbarWidth).toBeGreaterThan(0);
  const expandedBox = await heading.boundingBox();

  await page.getByRole("button", { name: "Collapse description" }).click();
  await expect(page.getByRole("button", { name: "Expand description" })).toBeVisible();

  expect((await scrollMetrics(scroller)).scrolls).toBe(false);
  expect((await heading.boundingBox())?.x).toBe(expandedBox?.x);
});
