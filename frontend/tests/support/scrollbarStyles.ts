import type { Locator } from "@playwright/test";

export interface AuthoredScrollbarWidth {
  selector: string;
  value: string;
}

export async function authoredScrollbarWidths(locator: Locator): Promise<AuthoredScrollbarWidth[]> {
  return locator.evaluate((element) => {
    const declarations: AuthoredScrollbarWidth[] = [];

    const collect = (rules: CSSRuleList): void => {
      for (const rule of rules) {
        if (rule instanceof CSSMediaRule && !matchMedia(rule.conditionText).matches) continue;
        if (rule instanceof CSSSupportsRule && !CSS.supports(rule.conditionText)) continue;

        if (rule instanceof CSSStyleRule) {
          const value = rule.style.getPropertyValue("scrollbar-width").trim();
          if (value) {
            try {
              if (element.matches(rule.selectorText)) {
                declarations.push({ selector: rule.selectorText, value });
              }
            } catch {
              // Selectors for another browser engine cannot match here.
            }
          }
        }

        if ("cssRules" in rule) {
          try {
            const nestedRules = rule.cssRules;
            if (nestedRules instanceof CSSRuleList) collect(nestedRules);
          } catch {
            // Cross-origin stylesheets are not inspectable.
          }
        }
      }
    };

    for (const sheet of document.styleSheets) {
      try {
        collect(sheet.cssRules);
      } catch {
        // Cross-origin stylesheets are not inspectable.
      }
    }

    const inlineValue = (element as HTMLElement).style.getPropertyValue("scrollbar-width").trim();
    if (inlineValue) declarations.push({ selector: "<inline>", value: inlineValue });

    return declarations;
  });
}
