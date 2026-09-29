import type { AgentCommand } from "./chat-types.js";

export type SlashToken = { query: string; end: number };

// The slash token while the caret is still inside the first word of a
// composer draft that starts with "/"; undefined once whitespace follows it.
export function slashToken(text: string, caret: number): SlashToken | undefined {
  if (!text.startsWith("/")) return undefined;
  const match = /\s/.exec(text);
  const end = match ? match.index : text.length;
  if (caret < 1 || caret > end) return undefined;
  return { query: text.slice(1, end), end };
}

// Case-insensitive prefix matches first, then substring matches, each in the
// agent's advertised order.
export function matchCommands(commands: readonly AgentCommand[], query: string): AgentCommand[] {
  const needle = query.toLowerCase();
  const prefix: AgentCommand[] = [];
  const inner: AgentCommand[] = [];
  for (const command of commands) {
    const name = command.name.toLowerCase();
    if (name.startsWith(needle)) prefix.push(command);
    else if (name.includes(needle)) inner.push(command);
  }
  return [...prefix, ...inner];
}

// Replaces the slash token with "/name " and returns the caret position just
// after the inserted command.
export function insertCommand(text: string, token: SlashToken, name: string): { text: string; caret: number } {
  const head = `/${name} `;
  return { text: head + text.slice(token.end).trimStart(), caret: head.length };
}

// Input hint for a draft that is exactly an inserted command awaiting input.
export function pendingInputHint(commands: readonly AgentCommand[], text: string): string {
  const match = /^\/(\S+) $/.exec(text);
  if (!match) return "";
  return commands.find((command) => command.name === match[1])?.inputHint ?? "";
}
