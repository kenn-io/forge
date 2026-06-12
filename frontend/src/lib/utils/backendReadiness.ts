import { getBasePath } from "../stores/router.svelte.js";

const BACKEND_READY_POLL_MS = 750;

function readinessPath(): string {
  const base = getBasePath().replace(/\/$/, "");
  return `${base}/healthz`;
}

function abortReason(signal: AbortSignal): unknown {
  return signal.reason ?? new DOMException("Aborted", "AbortError");
}

function sleep(ms: number, signal: AbortSignal): Promise<void> {
  if (signal.aborted) {
    return Promise.reject(abortReason(signal));
  }
  return new Promise((resolve, reject) => {
    const onAbort = () => {
      window.clearTimeout(timeout);
      reject(abortReason(signal));
    };
    const timeout = window.setTimeout(() => {
      signal.removeEventListener("abort", onAbort);
      resolve();
    }, ms);
    signal.addEventListener("abort", onAbort, { once: true });
  });
}

export async function waitUntilBackendReady(signal: AbortSignal): Promise<void> {
  const path = readinessPath();
  while (!signal.aborted) {
    try {
      const response = await fetch(path, {
        cache: "no-store",
        headers: { Accept: "application/json" },
        signal,
      });
      if (response.ok) return;
    } catch (err) {
      if (signal.aborted) throw err;
    }
    await sleep(BACKEND_READY_POLL_MS, signal);
  }
  throw abortReason(signal);
}
