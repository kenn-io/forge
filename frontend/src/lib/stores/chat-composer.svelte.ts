/**
 * Agent chat composers on screen that can take text from elsewhere on the
 * page, most recently focused last. "Add to chat" buttons render only while
 * one exists and append to the last one. A chat that appears or becomes ready
 * joins at the front, so it never takes over from the chat the user focused.
 */

import { untrack } from "svelte";

type Insert = (text: string) => void;

let composers = $state<Insert[]>([]);

// Registration runs inside the chat's effect; reading the list there would
// make the effect depend on the list it writes.
const others = (insert: Insert) => untrack(() => composers).filter((entry) => entry !== insert);

/** Publish a visible chat composer; returns the function that withdraws it. */
export function registerChatComposer(insert: Insert): () => void {
  composers = [insert, ...others(insert)];
  return () => {
    composers = others(insert);
  };
}

/** Make a registered composer the one "Add to chat" targets. */
export function noteChatComposerUsed(insert: Insert): void {
  if (composers.at(-1) === insert || !composers.includes(insert)) return;
  composers = [...others(insert), insert];
}

export function hasChatComposer(): boolean {
  return composers.length > 0;
}

/** Append text to the target chat's composer without sending it. */
export function addToChat(text: string): void {
  composers.at(-1)?.(text);
}
