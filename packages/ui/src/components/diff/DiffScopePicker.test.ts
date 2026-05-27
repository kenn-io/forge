import { compile } from "svelte/compiler";
import { describe, expect, it } from "vitest";
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
    const label = compiledStyle(pickerSource, ".diff-scope-picker__label");
    const scope = compiledStyle(labelSource, ".diff-scope-label");

    expect(trigger.getPropertyValue("line-height")).toBe("1");
    expect(label.getPropertyValue("display")).toBe("inline-flex");
    expect(label.getPropertyValue("line-height")).toBe("1");
    expect(scope.getPropertyValue("display")).toBe("inline-flex");
    expect(scope.getPropertyValue("line-height")).toBe("1");
  });
});
