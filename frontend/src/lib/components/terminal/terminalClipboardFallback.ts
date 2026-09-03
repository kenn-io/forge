import { configuredAPIBaseURL } from "../../api/runtime-base.js";

export async function writeTerminalClipboardThroughServer(text: string): Promise<void> {
  const response = await fetch(`${configuredAPIBaseURL()}/terminal/clipboard`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ text }),
  });
  if (!response.ok) {
    throw new Error(`terminal clipboard fallback failed (${response.status})`);
  }
}
