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
  const lines = [`diff --git a/${file.old_path || file.path} b/${file.path}`, `--- ${oldPath}`, `+++ ${newPath}`];
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

function normalizeFixtureFile(file: DiffFixtureFile): DiffFixtureFile {
  return {
    ...file,
    hunks: file.hunks.map((hunk) => ({
      ...hunk,
      old_count: hunk.lines.filter((line) => line.type !== "add").length,
      new_count: hunk.lines.filter((line) => line.type !== "delete").length,
    })),
  };
}

function withServerDiffData(fixture: DiffFixture): DiffResult {
  const files = fixture.files.map((file) => {
    const normalized = normalizeFixtureFile(file);
    return {
      ...normalized,
      patch: normalized.patch ?? patchForFile(normalized),
    };
  });
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
            {
              type: "context",
              content: "jobs:",
              old_num: 10,
              new_num: 10,
            },
            {
              type: "context",
              content: "  test:",
              old_num: 11,
              new_num: 11,
            },
            {
              type: "context",
              content: "    runs-on: ubuntu-latest",
              old_num: 12,
              new_num: 12,
            },
            {
              type: "delete",
              content: "    name: Run tests",
              old_num: 13,
            },
            {
              type: "add",
              content: "    name: Run tests with cross-browser coverage on multiple platforms and architectures",
              new_num: 13,
            },
            {
              type: "context",
              content: "    steps:",
              old_num: 14,
              new_num: 14,
            },
            {
              type: "delete",
              content: "      - run: go test ./...",
              old_num: 15,
            },
            {
              type: "add",
              content:
                '      - run: go build -o ./cmd/e2e-server/e2e-server ./cmd/e2e-server && playwright test --config playwright-e2e.config.ts --project=chromium --grep "UTC timestamp"',
              new_num: 15,
            },
            {
              type: "add",
              content:
                "      - run: playwright test --config playwright-e2e.config.ts --project=chromium --reporter=html --output=test-results/cross-browser-coverage",
              new_num: 16,
            },
            {
              type: "context",
              content: "  coverage:",
              old_num: 16,
              new_num: 17,
            },
            {
              type: "context",
              content: "    runs-on: ubuntu-latest",
              old_num: 17,
              new_num: 18,
            },
            {
              type: "delete",
              content: "      - run: go test -coverprofile=coverage.out ./...",
              old_num: 18,
            },
            {
              type: "delete",
              content: "      - run: go tool cover -html=coverage.out",
              old_num: 19,
            },
            {
              type: "add",
              content:
                "      - run: go test -coverprofile=coverage.out -covermode=atomic -race -shuffle=on -timeout=300s ./internal/... ./cmd/... 2>&1 | tee test-output.log",
              new_num: 19,
            },
            {
              type: "add",
              content:
                "      - run: go tool cover -html=coverage.out -o coverage-report.html && upload-artifact coverage-report.html coverage.out test-output.log",
              new_num: 20,
            },
            {
              type: "add",
              content:
                "      - run: playwright install --with-deps ${{ matrix.browser }} && playwright test --config playwright-e2e.config.ts --project=${{ matrix.browser }}",
              new_num: 21,
            },
            {
              type: "context",
              content: "    strategy:",
              old_num: 20,
              new_num: 22,
            },
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
  test("line backgrounds cover the rendered Pierre content width", async ({ page }) => {
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
    await page.locator(".diff-file").first().waitFor({ state: "visible", timeout: 10_000 });

    // Wait for syntax highlighting on both add and delete lines — highlighting
    // is incremental so we need both row types ready before asserting widths.
    const diffHost = page.locator(".pierre-diff").first();
    await expect
      .poll(
        async () => {
          return await diffHost.evaluate((host) => {
            return Boolean(host.shadowRoot?.querySelector('[data-content] [data-line-type="change-addition"]'));
          });
        },
        { timeout: 10_000 },
      )
      .toBe(true);
    await expect
      .poll(
        async () => {
          return await diffHost.evaluate((host) => {
            return Boolean(host.shadowRoot?.querySelector('[data-content] [data-line-type="change-deletion"]'));
          });
        },
        { timeout: 10_000 },
      )
      .toBe(true);

    // Verify the Pierre code grid and content column are present.
    const widths = await diffHost.evaluate((host) => {
      const root = host.shadowRoot;
      const pre = root?.querySelector("pre[data-diff]");
      const code = root?.querySelector("code[data-unified]");
      return {
        containerWidth: pre instanceof HTMLElement ? pre.clientWidth : 0,
        scrollWidth: pre instanceof HTMLElement ? pre.scrollWidth : 0,
        codeWidth: code ? code.getBoundingClientRect().width : 0,
      };
    });
    expect(widths.containerWidth).toBeGreaterThan(0);
    expect(widths.scrollWidth).toBeGreaterThan(0);
    expect(widths.codeWidth).toBeGreaterThan(0);

    // Verify individual add/delete content rows are sized to the scrollable
    // content column rather than the current viewport.
    const rowWidths = await diffHost.evaluate((host) => {
      const root = host.shadowRoot;
      const content = root?.querySelector("[data-content]");
      if (!content) {
        return {
          contentWidth: 0,
          addWidths: [] as number[],
          delWidths: [] as number[],
        };
      }
      const contentWidth = content.getBoundingClientRect().width;
      const adds = [...content.querySelectorAll('[data-line-type="change-addition"]')].map(
        (r) => r.getBoundingClientRect().width,
      );
      const dels = [...content.querySelectorAll('[data-line-type="change-deletion"]')].map(
        (r) => r.getBoundingClientRect().width,
      );
      return { contentWidth, addWidths: adds, delWidths: dels };
    });

    expect(rowWidths.addWidths.length).toBeGreaterThan(0);
    expect(rowWidths.delWidths.length).toBeGreaterThan(0);
    for (const w of [...rowWidths.addWidths, ...rowWidths.delWidths]) {
      expect(w).toBeGreaterThanOrEqual(rowWidths.contentWidth - 1);
    }
  });
});
