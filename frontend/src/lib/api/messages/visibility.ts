import type { MessagesCapabilities } from "./types.js";

export function shouldShowMessagesMode(capabilities: MessagesCapabilities | null): boolean {
  return capabilities?.configured === true;
}
