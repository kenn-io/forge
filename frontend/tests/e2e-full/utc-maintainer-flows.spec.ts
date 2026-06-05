import { expect, test } from "@playwright/test";

type PullRef = {
  owner: string;
  repo: string;
  number: number;
};

type IssueRef = {
  owner: string;
  repo: string;
  number: number;
};

async function fetchPullDetail(page: import("@playwright/test").Page, pull: PullRef) {
  return page.evaluate(async ({ owner, repo, number }) => {
    const response = await fetch(`/api/v1/pulls/github/${owner}/${repo}/${number}`);
    return response.json();
  }, pull);
}

async function fetchIssueDetail(page: import("@playwright/test").Page, issue: IssueRef) {
  return page.evaluate(async ({ owner, repo, number }) => {
    const response = await fetch(`/api/v1/issues/github/${owner}/${repo}/${number}`);
    return response.json();
  }, issue);
}

function widgetsPull(number: number): PullRef {
  return { owner: "acme", repo: "widgets", number };
}

function widgetsIssue(number: number): IssueRef {
  return { owner: "acme", repo: "widgets", number };
}

function toolsPull(number: number): PullRef {
  return { owner: "acme", repo: "tools", number };
}

function toolsIssue(number: number): IssueRef {
  return { owner: "acme", repo: "tools", number };
}

function closeReopenPullTarget(browserName: string): PullRef {
  switch (browserName) {
    case "chromium":
      return widgetsPull(6);
    case "firefox":
      return widgetsPull(2);
    case "webkit":
      return toolsPull(10);
    default:
      return widgetsPull(6);
  }
}

function mergeTarget(browserName: string): PullRef {
  switch (browserName) {
    case "chromium":
      return widgetsPull(7);
    case "firefox":
      return widgetsPull(1);
    case "webkit":
      return toolsPull(1);
    default:
      return widgetsPull(7);
  }
}

function closeReopenIssueTarget(browserName: string): IssueRef {
  switch (browserName) {
    case "chromium":
      return widgetsIssue(11);
    case "firefox":
      return widgetsIssue(10);
    case "webkit":
      return toolsIssue(5);
    default:
      return widgetsIssue(11);
  }
}

function expectUTCString(value: string | null): void {
  expect(value).toBeTruthy();
  expect(value).toMatch(/Z$/);
}

test.describe("UTC maintainer flows", () => {
  test("closing and reopening a pull request keeps API timestamps canonical UTC", async ({ page, browserName }) => {
    const pull = closeReopenPullTarget(browserName);

    await page.goto(`/pulls/github/${pull.owner}/${pull.repo}/${pull.number}`);
    await expect(page.locator(".btn--close")).toBeVisible();

    await page.locator(".btn--close").click();
    await expect(page.locator(".btn--reopen")).toBeVisible();

    const closed = await fetchPullDetail(page, pull);
    expect(closed.merge_request.State).toBe("closed");
    expectUTCString(closed.merge_request.ClosedAt);

    await page.locator(".btn--reopen").click();
    await expect(page.locator(".btn--close")).toBeVisible();

    const reopened = await fetchPullDetail(page, pull);
    expect(reopened.merge_request.State).toBe("open");
    expect(reopened.merge_request.ClosedAt).toBeNull();
  });

  test("merging a pull request stores UTC timestamps and updates the detail view", async ({ page, browserName }) => {
    const pull = mergeTarget(browserName);

    await page.goto(`/pulls/github/${pull.owner}/${pull.repo}/${pull.number}`);
    await expect(page.locator(".btn--merge")).toBeVisible();

    await page.locator(".btn--merge").click();
    await expect(page.locator(".modal-title")).toHaveText("Merge Pull Request");
    await page.locator(".btn--primary.btn--green").click();

    await expect(page.locator(".chip.chip--purple")).toHaveText("Merged");

    const merged = await fetchPullDetail(page, pull);
    expect(merged.merge_request.State).toBe("merged");
    expectUTCString(merged.merge_request.ClosedAt);
    expectUTCString(merged.merge_request.MergedAt);
  });

  test("closing and reopening an issue keeps API timestamps canonical UTC", async ({ page, browserName }) => {
    const issue = closeReopenIssueTarget(browserName);

    await page.goto(`/issues/github/${issue.owner}/${issue.repo}/${issue.number}`);
    await expect(page.locator(".btn--close")).toBeVisible();

    await page.locator(".btn--close").click();
    await expect(page.locator(".btn--reopen")).toBeVisible();

    const closed = await fetchIssueDetail(page, issue);
    expect(closed.issue.State).toBe("closed");
    expectUTCString(closed.issue.ClosedAt);

    await page.locator(".btn--reopen").click();
    await expect(page.locator(".btn--close")).toBeVisible();

    const reopened = await fetchIssueDetail(page, issue);
    expect(reopened.issue.State).toBe("open");
    expect(reopened.issue.ClosedAt).toBeNull();
  });
});
