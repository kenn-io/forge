import type { HarnessIconId } from "@kenn-io/kit-ui";

interface CommitAgent {
  harness: HarnessIconId;
  name: string;
}

interface CommitAttribution {
  message: string;
  agents: CommitAgent[];
}

const AGENTS: readonly (CommitAgent & { pattern: RegExp })[] = [
  { harness: "openai", name: "Codex", pattern: /^(?:OpenAI )?Codex(?: \([^\r\n()]+\))?$/i },
  {
    harness: "claude",
    name: "Claude Code",
    pattern: /^Claude(?: Code| (?:Opus|Sonnet|Haiku|Fable) [\d.]+)?(?: \([^\r\n()]+\))?$/i,
  },
  { harness: "pi", name: "Pi", pattern: /^Pi(?: \([^\r\n()]+\))?$/i },
  { harness: "grok", name: "Grok", pattern: /^Grok(?: \([^\r\n()]+\))?$/i },
];

/** Extract only named agent attribution; the original message stays with the event. */
export function commitAttribution(body: string): CommitAttribution {
  const agents: CommitAgent[] = [];
  let fence = "";
  const message = body
    .split(/\r?\n/)
    .filter((line, index) => {
      // The subject and examples inside fenced blocks are commit content.
      if (index === 0) return true;
      const marker = line.match(/^\s*(`{3,}|~{3,})/)?.[1];
      if (marker) {
        if (!fence) fence = marker;
        else if (marker[0] === fence[0] && marker.length >= fence.length) fence = "";
        return true;
      }
      if (fence) return true;

      const text = line
        .trim()
        .replace(/^<sup>(.*)<\/sup>$/i, "$1")
        .replace(/^\u{1f916}\s*/u, "");
      const generated = text.match(/^Generated with (.+)$/i);
      const coauthor = text.match(/^Co-authored-by:\s*(.+?)\s+<[^<>\s]+>$/i);
      let name = generated?.[1] ?? coauthor?.[1];
      if (!name) return true;
      if (generated) name = name.replace(/^\[([^\]]+)\]\(https?:\/\/[^\s)]+\)$/, "$1");
      const agentName = name;
      const agent = AGENTS.find((candidate) => candidate.pattern.test(agentName));
      if (!agent) return true;
      if (!agents.some((existing) => existing.harness === agent.harness)) {
        agents.push({ harness: agent.harness, name: agent.name });
      }
      return false;
    })
    .join("\n")
    .trim();
  return { message, agents };
}
