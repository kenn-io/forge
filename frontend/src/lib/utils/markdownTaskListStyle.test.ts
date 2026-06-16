import { existsSync, readFileSync } from "node:fs";
import { describe, expect, test } from "vite-plus/test";

const appCssPath = existsSync("src/app.css") ? "src/app.css" : "frontend/src/app.css";
const appCss = readFileSync(appCssPath, "utf8");

function declarationsFor(selector: string): Map<string, string> {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const match = appCss.match(new RegExp(`${escaped}\\s*\\{([^}]*)\\}`));
  if (match?.[1]) {
    return new Map(
      match[1]
        .split(";")
        .map((declaration) => declaration.trim())
        .filter(Boolean)
        .map((declaration) => {
          const separator = declaration.indexOf(":");
          return [declaration.slice(0, separator).trim(), declaration.slice(separator + 1).trim()];
        }),
    );
  }
  throw new Error(`Missing CSS rule for ${selector}`);
}

describe("markdown task list styles", () => {
  test("keeps checkbox labels in normal inline flow for long PR descriptions", () => {
    const itemStyle = declarationsFor(".markdown-body .task-list-item");

    expect(itemStyle.get("display")).toBe("block");
    expect(itemStyle.has("flex-wrap")).toBe(false);
  });
});
