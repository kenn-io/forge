import { compile } from "svelte/compiler";
import { describe, expect, it } from "vite-plus/test";
import labelSource from "./DiffScopeLabel.svelte?raw";
import pickerSource from "./DiffScopePicker.svelte?raw";

function compiledStyle(source: string, selector: string): CSSStyleDeclaration {
  const css = compile(source, { filename: "component.svelte" }).css?.code ?? "";
  const style = document.createElement("style");
  style.textContent = css;
  document.head.appendChild(style);

  for (const rule of Array.from(style.sheet?.cssRules ?? [])) {
    if (!("selectorText" in rule) || !("style" in rule)) continue;
    if (String(rule.selectorText).includes(selector)) {
      return rule.style as CSSStyleDeclaration;
    }
  }
  throw new Error(`Could not find compiled style rule for ${selector}`);
}

describe("DiffScopePicker", () => {
  it("keeps toolbar labels vertically centered", () => {
    const trigger = compiledStyle(pickerSource, ".diff-scope-picker__trigger");
    const pickerScope = compiledStyle(pickerSource, ".diff-scope-picker__trigger .diff-scope-label");
    const scope = compiledStyle(labelSource, ".diff-scope-label");

    expect(trigger.getPropertyValue("line-height")).toBe("1");
    expect(pickerScope.getPropertyValue("font-size")).toBe("var(--font-size-xs)");
    expect(scope.getPropertyValue("display")).toBe("inline-flex");
    expect(scope.getPropertyValue("line-height")).toBe("1");
  });
});
