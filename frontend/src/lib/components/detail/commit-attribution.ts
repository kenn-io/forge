import { HARNESS_ICONS, type HarnessIconId } from "@kenn-io/kit-ui";

interface CommitAgent {
  harness: HarnessIconId;
  name: string;
}

interface CommitAttribution {
  message: string;
  agents: CommitAgent[];
}

function withoutModelSuffix(name: string): string {
  return name.replace(/ \([^()\r\n]+\)$/, "");
}

function nameKey(name: string): string {
  return name.toLowerCase().replace(/[\s._-]+/g, "");
}

const AGENTS = new Map<string, CommitAgent>(
  HARNESS_ICONS.flatMap((icon) => {
    const primaryName = withoutModelSuffix(icon.agents[0] ?? icon.label);
    return [
      ...[icon.id, icon.label].map((name) => [nameKey(name), { harness: icon.id, name: primaryName }] as const),
      ...icon.agents.map((name) => {
        const productName = withoutModelSuffix(name);
        return [nameKey(productName), { harness: icon.id, name: productName }] as const;
      }),
    ];
  }),
);

function agentForAttribution(name: string): CommitAgent | undefined {
  const product = withoutModelSuffix(name.replace(/^\[([^\]]+)\]\(https?:\/\/[^\s)]+\)(.*)$/, "$1$2"));
  // These commit signatures name the vendor or model rather than the catalog product.
  const knownSignature = product
    .replace(/^OpenAI Codex$/i, "Codex")
    .replace(/^Claude (?:Opus|Sonnet|Haiku|Fable) [\d.]+$/i, "Claude");
  return AGENTS.get(nameKey(knownSignature));
}

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
      const generated = text.match(/^Generated (?:with|by) (.+)$/i);
      const coauthor = text.match(/^Co-authored-by:\s*(.+?)\s+<[^<>\s]+>$/i);
      const name = generated?.[1] ?? coauthor?.[1];
      if (!name) return true;
      const agent = agentForAttribution(name);
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
