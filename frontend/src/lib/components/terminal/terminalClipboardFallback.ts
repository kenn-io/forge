import { writeTerminalClipboard } from "../../api/generated/default/default.js";

export async function writeTerminalClipboardThroughServer(text: string): Promise<void> {
  await writeTerminalClipboard(
    { text },
    {
      fetch: async (input, init) => {
        const response = await fetch(input, init);
        if (!response.ok) {
          throw new Error(`terminal clipboard fallback failed (${response.status})`);
        }
        return response;
      },
    },
  );
}
