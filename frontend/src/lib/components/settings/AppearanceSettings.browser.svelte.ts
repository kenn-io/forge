import { afterEach, beforeEach, expect, it } from "vite-plus/test";
import { page } from "vite-plus/test/browser";
import { render } from "vitest-browser-svelte";
import { cleanupTheme, initTheme } from "../../stores/theme.svelte.js";
import AppearanceSettings from "./AppearanceSettings.svelte";
import "../../../app.css";

beforeEach(() => {
  localStorage.setItem("kenn-forge-theme", "light");
  initTheme();
});

afterEach(() => {
  localStorage.removeItem("kenn-forge-theme");
  localStorage.removeItem("kenn-forge-theme-name");
  localStorage.removeItem("kenn-forge-theme-high-contrast");
  initTheme();
  cleanupTheme();
});

it("switches light/dark and restores the selected contrast", async () => {
  await render(AppearanceSettings);
  await page.getByRole("combobox", { name: "Color theme: Light" }).click();
  await page.getByRole("option", { name: "Dark", exact: true }).click();
  await page.getByRole("switch", { name: "High contrast" }).click();
  cleanupTheme();
  initTheme();
  expect(document.documentElement.classList.contains("dark")).toBe(true);
  expect(document.documentElement.classList.contains("high-contrast")).toBe(true);
  await page.getByRole("combobox", { name: "Color theme: Dark" }).click();
  await page.getByRole("option", { name: "Light", exact: true }).click();
  expect(document.documentElement.classList.contains("dark")).toBe(false);
});

it("paints e-reader controls with black ink and white surfaces after restoration", async () => {
  const { container } = await render(AppearanceSettings);
  await page.getByRole("combobox", { name: "Color theme: Light" }).click();
  await page.getByRole("option", { name: "E-reader" }).click();
  cleanupTheme();
  initTheme();
  await expect.element(page.getByRole("combobox", { name: "Color theme: E-reader" })).toBeVisible();
  const button = page.getByRole("combobox", { name: "Color theme: E-reader" }).element();
  expect(getComputedStyle(document.body).backgroundColor).toBe("rgb(255, 255, 255)");
  expect(getComputedStyle(button).color).toBe("rgb(0, 0, 0)");
  expect(getComputedStyle(button).borderTopColor).toBe("rgb(0, 0, 0)");
  expect(getComputedStyle(container.querySelector("p")!).color).toBe("rgb(0, 0, 0)");
});

it("keeps accent ink in color e-reader mode but forces black ink in plain e-reader mode", async () => {
  const { container } = await render(AppearanceSettings);
  const accent = document.createElement("span");
  accent.textContent = "Passing";
  accent.style.color = "var(--accent-green)";
  container.append(accent);

  await page.getByRole("combobox", { name: "Color theme: Light" }).click();
  await page.getByRole("option", { name: "Color e-reader" }).click();
  await expect.element(page.getByText(/color kept for status/)).toBeVisible();
  expect(getComputedStyle(document.body).backgroundColor).toBe("rgb(255, 255, 255)");
  expect(getComputedStyle(accent).color).toBe("rgb(4, 112, 79)");

  await page.getByRole("combobox", { name: "Color theme: Color e-reader" }).click();
  await page.getByRole("option", { name: "E-reader", exact: true }).click();
  expect(getComputedStyle(accent).color).toBe("rgb(0, 0, 0)");
});
