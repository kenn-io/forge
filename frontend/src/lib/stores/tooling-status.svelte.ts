// Tooling status with a standalone fallback. Embedded, the host
// pushes its own probe into the embed config slot
// (__middleman_update_tooling) and that value wins. Standalone, no
// embedder exists to fill the slot, so the first read lazily fetches
// the server's native probe from GET /api/v1/tooling-status. The
// fetch fires once per page load — the server caches probes
// internally, and tool/auth state changes rarely.

import { getToolingStatus, isEmbedded, type ToolingStatusValue } from "./embed-config.svelte.ts";
import { client } from "../api/runtime.js";

type ToolingFetcher = () => Promise<ToolingStatusValue | undefined>;

async function fetchServerToolingStatus(): Promise<ToolingStatusValue | undefined> {
  const { data } = await client.GET("/tooling-status");
  return data ?? undefined;
}

let fetched = $state<ToolingStatusValue | undefined>(undefined);
let fetchStarted = false;
let fetcher: ToolingFetcher = fetchServerToolingStatus;

// resolveToolingStatus returns the embedder's tooling status when one
// is configured, falling back to the server's native probe in
// standalone mode. Reading it from a $derived keeps consumers
// reactive to both the embed-config push and the fetch completing.
export function resolveToolingStatus(): ToolingStatusValue | undefined {
  if (isEmbedded()) {
    return getToolingStatus();
  }
  startServerFetch();
  return fetched;
}

function startServerFetch(): void {
  if (fetchStarted) return;
  fetchStarted = true;
  void fetcher().then(
    (status) => {
      if (status) fetched = status;
    },
    () => {
      // Probe failure leaves the status unknown; consumers render
      // their hideWhenUnknown / "tooling unavailable" fallbacks.
    },
  );
}

// resetToolingStatusForTest clears module state and optionally swaps
// the fetcher so tests can run without a server.
export function resetToolingStatusForTest(fetch?: ToolingFetcher): void {
  fetched = undefined;
  fetchStarted = false;
  fetcher = fetch ?? fetchServerToolingStatus;
}
