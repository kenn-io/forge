/**
 * Agent chat composers on screen that can take text from elsewhere on the
 * page, most recently used last. "Add to chat" buttons render only while one
 * exists and append to the last one, which is the chat the user last focused
 * when several are visible.
 */

import { untrack } from "svelte";

type Insert = (text: string) => void;

let composers = $state<Insert[]>([]);

// Registration runs inside the chat's effect; reading the list there would
// make the effect depend on the list it writes.
function moveToEnd(insert: Insert): void {
  composers = [...untrack(() => composers).filter((entry) => entry !== insert), insert];
}

/** Publish a visible chat composer; returns the function that withdraws it. */
export function registerChatComposer(insert: Insert): () => void {
  moveToEnd(insert);
  return () => {
    composers = untrack(() => composers).filter((entry) => entry !== insert);
  };
}

/** Make a registered composer the one "Add to chat" targets. */
export function noteChatComposerUsed(insert: Insert): void {
  if (composers.at(-1) === insert || !composers.includes(insert)) return;
  moveToEnd(insert);
}

export function hasChatComposer(): boolean {
  return composers.length > 0;
}

/** Append text to the target chat's composer without sending it. */
export function addToChat(text: string): void {
  composers.at(-1)?.(text);
}
