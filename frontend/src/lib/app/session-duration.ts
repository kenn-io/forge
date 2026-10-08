const HIDDEN_SESSION_TIMEOUT_MS = 30 * 60_000;

function durationBucket(visibleMs: number): string {
  if (visibleMs < 60_000) return "under_1m";
  if (visibleMs < 5 * 60_000) return "1_to_5m";
  if (visibleMs <= 30 * 60_000) return "5_to_30m";
  if (visibleMs <= 2 * 60 * 60_000) return "30m_to_2h";
  return "over_2h";
}

export function startSessionDurationReporting(send: (bucket: string) => void): () => void {
  let visibleMs = 0;
  let started = document.hidden ? undefined : performance.now();
  let hiddenTimer: ReturnType<typeof setTimeout> | undefined;
  let hiddenAt: number | undefined;

  const pause = () => {
    if (started === undefined) return;
    visibleMs += performance.now() - started;
    started = undefined;
  };
  const end = () => {
    clearTimeout(hiddenTimer);
    hiddenAt = undefined;
    pause();
    if (visibleMs > 0) send(durationBucket(visibleMs));
    visibleMs = 0;
  };
  const resume = () => {
    if (document.hidden) return;
    if (hiddenAt !== undefined && Date.now() - hiddenAt >= HIDDEN_SESSION_TIMEOUT_MS) end();
    clearTimeout(hiddenTimer);
    hiddenAt = undefined;
    if (started === undefined) started = performance.now();
  };
  const visibility = () => {
    if (document.hidden) {
      pause();
      hiddenAt = Date.now();
      hiddenTimer = setTimeout(end, HIDDEN_SESSION_TIMEOUT_MS);
    } else {
      resume();
    }
  };
  document.addEventListener("visibilitychange", visibility);
  window.addEventListener("pagehide", end);
  window.addEventListener("pageshow", resume);
  return () => {
    clearTimeout(hiddenTimer);
    document.removeEventListener("visibilitychange", visibility);
    window.removeEventListener("pagehide", end);
    window.removeEventListener("pageshow", resume);
  };
}
