import { expect, test } from "@playwright/test";
import type { DiffFile, DiffLine, DiffResult, FilesResult } from "@middleman/ui/api/types";

type DiffFixtureFile = Omit<DiffFile, "patch"> & { patch?: string };
type DiffFixture = Omit<DiffResult, "files"> & {
  files: DiffFixtureFile[];
};

function patchLinePrefix(line: DiffLine): string {
  if (line.type === "add") return "+";
  if (line.type === "delete") return "-";
  return " ";
}

function patchRange(start: number, count: number): string {
  return count === 1 ? `${start}` : `${start},${count}`;
}

function patchForFile(file: DiffFixtureFile): string {
  if (file.is_binary || file.hunks.length === 0) return "";
  const oldPath = file.status === "added" ? "/dev/null" : `a/${file.old_path || file.path}`;
  const newPath = file.status === "deleted" ? "/dev/null" : `b/${file.path}`;
  const lines = [
    `diff --git a/${file.old_path || file.path} b/${file.path}`,
    `--- ${oldPath}`,
    `+++ ${newPath}`,
  ];
  for (const hunk of file.hunks) {
    lines.push(
      `@@ -${patchRange(hunk.old_start, hunk.old_count)} +${patchRange(hunk.new_start, hunk.new_count)} @@${hunk.section ? ` ${hunk.section}` : ""}`,
    );
    for (const line of hunk.lines) {
      lines.push(`${patchLinePrefix(line)}${line.content}`);
    }
  }
  return `${lines.join("\n")}\n`;
}

function withServerDiffData(fixture: DiffFixture): DiffResult {
  const files = fixture.files.map((file) => ({
    ...file,
    patch: file.patch ?? patchForFile(file),
  }));
  return {
    ...fixture,
    files,
  };
}

// Fixture with long lines that force horizontal scroll.
const longLineDiff: DiffResult = withServerDiffData({
  stale: false,
  whitespace_only_count: 0,
  files: [
    {
      path: ".github/workflows/ci.yml",
      old_path: ".github/workflows/ci.yml",
      status: "modified",
      is_binary: false,
      is_whitespace_only: false,
      additions: 8,
      deletions: 4,
      hunks: [
        {
          old_start: 10,
          old_count: 12,
          new_start: 10,
          new_count: 16,
          section: "jobs",
          lines: [
            { type: "context", content: "jobs:", old_num: 10, new_num: 10 },
            { type: "context", content: "  test:", old_num: 11, new_num: 11 },
            { type: "context", content: "    runs-on: ubuntu-latest", old_num: 12, new_num: 12 },
            { type: "delete", content: "    name: Run tests", old_num: 13 },
            { type: "add", content: "    name: Run tests with cross-browser coverage on multiple platforms and architectures", new_num: 13 },
            { type: "context", content: "    steps:", old_num: 14, new_num: 14 },
            { type: "delete", content: "      - run: go test ./...", old_num: 15 },
            { type: "add", content: "      - run: go build -o ./cmd/e2e-server/e2e-server ./cmd/e2e-server && playwright test --config playwright-e2e.config.ts --project=chromium --grep \"UTC timestamp\"", new_num: 15 },
            { type: "add", content: "      - run: playwright test --config playwright-e2e.config.ts --project=chromium --reporter=html --output=test-results/cross-browser-coverage", new_num: 16 },
            { type: "context", content: "  coverage:", old_num: 16, new_num: 17 },
            { type: "context", content: "    runs-on: ubuntu-latest", old_num: 17, new_num: 18 },
            { type: "delete", content: "      - run: go test -coverprofile=coverage.out ./...", old_num: 18 },
            { type: "delete", content: "      - run: go tool cover -html=coverage.out", old_num: 19 },
            { type: "add", content: "      - run: go test -coverprofile=coverage.out -covermode=atomic -race -shuffle=on -timeout=300s ./internal/... ./cmd/... 2>&1 | tee test-output.log", new_num: 19 },
            { type: "add", content: "      - run: go tool cover -html=coverage.out -o coverage-report.html && upload-artifact coverage-report.html coverage.out test-output.log", new_num: 20 },
            { type: "add", content: "      - run: playwright install --with-deps ${{ matrix.browser }} && playwright test --config playwright-e2e.config.ts --project=${{ matrix.browser }}", new_num: 21 },
            { type: "context", content: "    strategy:", old_num: 20, new_num: 22 },
          ],
        },
      ],
    },
  ],
});

function filesFromDiff(fixture: DiffResult): FilesResult {
  return {
    stale: fixture.stale,
    files: fixture.files.map((f) => ({
      ...f,
      additions: 0,
      deletions: 0,
      hunks: [],
    })),
  };
}

test.describe("diff highlight backgrounds on horizontal scroll", () => {
  test("line backgrounds extend to full scroll width", async ({ page }) => {
    await page.route("**/api/v1/pulls/github/acme/widgets/1/files", async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(filesFromDiff(longLineDiff)),
      });
    });
    await page.route("**/api/v1/pulls/github/acme/widgets/1/diff*", async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(longLineDiff),
      });
    });

    await page.goto("/pulls/github/acme/widgets/1/files");
    await page.locator(".diff-file").first()
      .waitFor({ state: "visible", timeout: 10_000 });

    // Wait for syntax highlighting on both add and delete lines — highlighting
    // is incremental so we need both row types ready before asserting widths.
    await page.locator(".diff-line--add .code span[style]").first()
      .waitFor({ state: "attached", timeout: 10_000 });
    await page.locator(".diff-line--del .code span[style]").first()
      .waitFor({ state: "attached", timeout: 10_000 });

    const fileContent = page.locator(".file-content").first();

    // Scroll the diff area to the right and wait for scroll to settle.
    await fileContent.evaluate((el) => { el.scrollLeft = 300; });
    await expect.poll(
      () => fileContent.evaluate((el) => el.scrollLeft),
      { timeout: 2_000 },
    ).toBeGreaterThan(0);

    // Verify .file-rows wrapper is wider than the visible container.
    const widths = await fileContent.evaluate((el) => {
      const rows = el.querySelector(".file-rows");
      return {
        containerWidth: el.clientWidth,
        scrollWidth: el.scrollWidth,
        rowsWidth: rows ? rows.getBoundingClientRect().width : 0,
      };
    });
    expect(widths.scrollWidth).toBeGreaterThan(widths.containerWidth);
    expect(widths.rowsWidth).toBeGreaterThanOrEqual(widths.scrollWidth - 1);

    // Verify individual add/delete rows match the file-rows width (backgrounds
    // extend to the full scroll width, not just the viewport).
    const rowWidths = await fileContent.evaluate((el) => {
      const rows = el.querySelector(".file-rows");
      if (!rows) return { rowsWidth: 0, addWidths: [] as number[], delWidths: [] as number[] };
      const rw = rows.getBoundingClientRect().width;
      const adds = [...el.querySelectorAll(".diff-line--add")].map(
        (r) => r.getBoundingClientRect().width,
      );
      const dels = [...el.querySelectorAll(".diff-line--del")].map(
        (r) => r.getBoundingClientRect().width,
      );
      return { rowsWidth: rw, addWidths: adds, delWidths: dels };
    });

    expect(rowWidths.addWidths.length).toBeGreaterThan(0);
    expect(rowWidths.delWidths.length).toBeGreaterThan(0);
    for (const w of [...rowWidths.addWidths, ...rowWidths.delWidths]) {
      expect(w).toBeGreaterThanOrEqual(rowWidths.rowsWidth - 1);
    }
  });
});
