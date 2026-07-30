interface TerminalTextureAtlasParticipant {
  readonly rows: number;
  clearTextureAtlas(): void;
  refresh(start: number, end: number): void;
}

const participants = new Set<TerminalTextureAtlasParticipant>();

export function registerTerminalTextureAtlasParticipant(terminal: TerminalTextureAtlasParticipant): () => void {
  participants.add(terminal);
  return () => participants.delete(terminal);
}

/**
 * Clear one terminal's atlas and repaint every other terminal that may share it.
 *
 * xterm's WebGL atlas is shared between terminals with matching render options.
 * In xterm 0.19, clearing that atlas only invalidates the terminal that initiated
 * the clear, leaving sibling render models pointing at repurposed glyph slots.
 * The caller already repaints itself as part of its font-metric update, so only
 * the sibling terminals need an explicit refresh here.
 */
export function clearSharedTerminalTextureAtlas(source: TerminalTextureAtlasParticipant): void {
  source.clearTextureAtlas();
  for (const terminal of participants) {
    if (terminal === source) continue;
    terminal.refresh(0, Math.max(0, terminal.rows - 1));
  }
}
