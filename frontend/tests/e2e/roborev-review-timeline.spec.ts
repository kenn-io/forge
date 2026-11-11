import { expect, test } from "@playwright/test";
import { mockPullFixture } from "../../src/test/mockApiFetch";
import { mockApi } from "./support/mockApi";

const head = "96027d7aa1b2c3d4e5f60718293a4b5c6d7e8f90";

function roborevComment(id: number, sha: string, verdict: string, createdAt: string) {
  return {
    ID: id,
    MergeRequestID: 42,
    PlatformID: id,
    PlatformExternalID: String(id),
    EventType: "issue_comment",
    Author: "roborev-ci[bot]",
    Summary: "",
    Body: `<!-- roborev-pr-comment -->\n## roborev: Combined Review (\`${sha}\`)\n\n**Verdict:** ${verdict}\n`,
    MetadataJSON: "",
    CreatedAt: createdAt,
    DedupeKey: `comment-${id}`,
    DirectURL: `https://github.com/acme/widgets/pull/42#issuecomment-${id}`,
    ThreadID: null,
    Resolvable: false,
    Resolved: false,
  };
}

test("opens a pull request's roborev reviews from the detail chip", async ({ page }) => {
  await mockApi(page);
  const pull = mockPullFixture(42);
  await page.route("**/api/v1/pulls/github/acme/widgets/42", async (route) => {
    await route.fulfill({
      json: {
        merge_request: { ...pull, platform_head_sha: head },
        repo: { ...pull.repo, platform_repo_id: 1001 },
        repo_owner: "acme",
        repo_name: "widgets",
        platform_host: "github.com",
        platform_head_sha: head,
        reviewed_head_sha: head,
        detail_loaded: true,
        events: [
          roborevComment(501, "180f8e3", "Changes require fixes for 2 findings.", "2026-11-10T18:00:00Z"),
          roborevComment(502, "96027d7", "No findings at or above medium severity.", "2026-11-10T21:00:00Z"),
        ],
      },
    });
  });

  await page.goto("/pulls/github/acme/widgets/42");
  await page.getByTestId("roborev-chip").click();

  const rows = page.getByTestId("roborev-timeline").getByRole("listitem");
  await expect(rows).toHaveCount(2);
  await expect(rows.nth(0)).toContainText("2 findings");
  await expect(rows.nth(0)).toContainText("Superseded");
  await expect(rows.nth(1)).toContainText("Clean");
  await expect(rows.nth(1)).toContainText("Current");

  await rows.nth(1).getByRole("button").click();
  const drawer = page.getByRole("region", { name: "Review details" });
  await expect(drawer.getByText("No findings at or above medium severity.")).toBeVisible();
  await expect(drawer.getByRole("link", { name: "Comment" })).toHaveAttribute(
    "href",
    "https://github.com/acme/widgets/pull/42#issuecomment-502",
  );
});
