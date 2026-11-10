/**
 * Agent chat composers on screen that can take text from elsewhere on the
 * page. "Add to chat" buttons render only while one exists and append to the
 * one the user focused last. Each chat keeps its own focus time, so focus
 * before it was ready, or before it was hidden, still counts once it is back.
 */

import { untrack } from "svelte";

export interface ChatComposer {
  insert: (text: string) => void;
  /** When the user last focused this chat; 0 if never. */
  lastFocused: () => number;
}

// Raw: a deep proxy would wrap each entry, and unregistering compares identity.
let composers = $state.raw<ChatComposer[]>([]);

/** Publish a visible chat composer; returns the function that withdraws it. */
export function registerChatComposer(composer: ChatComposer): () => void {
  // Registration runs inside the chat's effect; reading the list there would
  // make the effect depend on the list it writes.
  composers = [...untrack(() => composers), composer];
  return () => {
    composers = untrack(() => composers).filter((entry) => entry !== composer);
  };
}

// A counter, not a clock: browsers coarsen timers, so two focuses in one
// task could otherwise tie.
let focusSequence = 0;

/** A value for ChatComposer.lastFocused that is later than every earlier one. */
export function nextChatFocus(): number {
  return ++focusSequence;
}

export function hasChatComposer(): boolean {
  return composers.length > 0;
}

/** Append text to the last-focused chat's composer without sending it. */
export function addToChat(text: string): void {
  const target = composers.reduce<ChatComposer | undefined>(
    (best, entry) => (best && best.lastFocused() >= entry.lastFocused() ? best : entry),
    undefined,
  );
  target?.insert(text);
}
