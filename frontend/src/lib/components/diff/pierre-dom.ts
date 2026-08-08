export type RenderedCodeSide = "deletions" | "additions";

export function renderedCodeColumns(pre: Element): Element[] {
  return Array.from(pre.children).filter((child) => child.tagName.toLowerCase() === "code");
}

export function renderedCodeSide(code: Element): RenderedCodeSide | undefined {
  if (code.hasAttribute("data-deletions")) return "deletions";
  if (code.hasAttribute("data-additions")) return "additions";

  const pre = code.parentElement;
  if (!pre) return undefined;
  const codeIndex = renderedCodeColumns(pre).indexOf(code);
  if (codeIndex === 0) return "deletions";
  if (codeIndex === 1) return "additions";
  return undefined;
}
