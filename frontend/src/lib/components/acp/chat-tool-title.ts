// Splits an ACP tool call title into its verb and its target so a row can show
// the target (a path, URL, or pattern) in code font. The shapes come from the
// titles claude-agent-acp 0.100.0 and the upstream Codex ACP source build:
// "Read src/app.ts (1 - 50)", "Find `src` `*.ts`", "Search foo in src",
// "Fetch https://example.com". Titles of any other shape stay plain text.

export type ToolTitlePart = { text: string; role: "target" | "plain" | "range" };
export type ToolTitle = { shell: true } | { shell: false; parts: ToolTitlePart[] };

// Each verb with the tool kinds the adapters send it under.
const VERBS: Readonly<Record<string, readonly string[]>> = {
  Read: ["read"],
  Write: ["edit"],
  Edit: ["edit"],
  Delete: ["delete"],
  Move: ["move"],
  List: ["search"],
  Find: ["search"],
  Search: ["search", "fetch"],
  Fetch: ["fetch"],
};
const READ_RANGE = /^(.+?)( \((?:\d+ - \d+|from line \d+)\))$/;

export function parseToolTitle(kind: string | null | undefined, title: string): ToolTitle {
  // Claude's Grep tool reports the equivalent grep command line as a search.
  if (kind === "execute" || (kind === "search" && title.startsWith("grep "))) return { shell: true };
  const verb = Object.keys(VERBS).find(
    (candidate) => title.startsWith(`${candidate} `) && !!kind && VERBS[candidate]!.includes(kind),
  );
  const target = verb && title.slice(verb.length + 1);
  if (!verb || !target) return { shell: false, parts: [{ text: title, role: "plain" }] };
  return {
    shell: false,
    parts: [{ text: `${verb} `, role: "plain" }, ...targetParts(kind, target)],
  };
}

function targetParts(kind: string | null | undefined, target: string): ToolTitlePart[] {
  // Claude's Find quotes its path and pattern in backticks.
  if (target.includes("`")) {
    return target
      .split("`")
      .map((text, index): ToolTitlePart => ({ text, role: index % 2 ? "target" : "plain" }))
      .filter((part) => part.text);
  }
  const range = kind === "read" ? READ_RANGE.exec(target) : null;
  if (range)
    return [
      { text: range[1]!, role: "target" },
      { text: range[2]!, role: "range" },
    ];
  // Codex titles a search "Search <query> in <path>". A path rarely holds " in ".
  const scope = kind === "search" ? target.lastIndexOf(" in ") : -1;
  if (scope > 0) {
    return [
      { text: target.slice(0, scope), role: "target" },
      { text: " in ", role: "plain" },
      { text: target.slice(scope + 4), role: "target" },
    ];
  }
  return [{ text: target, role: "target" }];
}
