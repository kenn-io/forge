import { csrfFetch } from "../../api/csrf.js";
import { configuredAPIBaseURL } from "../../api/runtime-base.js";

export async function writeTerminalClipboardThroughServer(text: string): Promise<void> {
  const response = await csrfFetch(fetch)(`${configuredAPIBaseURL()}/terminal/clipboard`, {
    method: "POST",
    body: JSON.stringify({ text }),
  });
  if (!response.ok) {
    throw new Error(`terminal clipboard fallback failed (${response.status})`);
  }
}
