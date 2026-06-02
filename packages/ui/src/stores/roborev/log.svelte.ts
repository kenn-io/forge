import type { RoborevClient } from "../../api/roborev/client.js";

interface LogLine {
  ts: string;
  text: string;
  lineType: string;
}

export interface LogStoreOptions {
  client: RoborevClient;
  baseUrl: string;
}

export function createLogStore(opts: LogStoreOptions) {
  const client = opts.client;

  let lines = $state<LogLine[]>([]);
  let streaming = $state(false);
  let followMode = $state(true);
  let connectedJobId = $state<number | undefined>(undefined);
  let abortController: AbortController | null = null;
  let requestVersion = 0;

  async function startStreaming(jobId: number): Promise<void> {
    stopStreaming();
    const version = ++requestVersion;
    connectedJobId = jobId;
    streaming = true;
    lines = [];

    abortController = new AbortController();
    const url = `${opts.baseUrl}/api/job/log?job_id=${jobId}`;

    try {
      const resp = await fetch(url, {
        signal: abortController.signal,
      });
      if (!resp.ok || !resp.body) {
        if (version === requestVersion) {
          streaming = false;
        }
        return;
      }

      const reader = resp.body.getReader();
      const decoder = new TextDecoder();
      let buffer = "";

      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        if (version !== requestVersion) break;
        buffer += decoder.decode(value, {
          stream: true,
        });

        const parts = buffer.split("\n");
        buffer = parts.pop() ?? "";
        for (const part of parts) {
          const trimmed = part.trim();
          if (!trimmed) continue;
          try {
            const parsed = JSON.parse(trimmed);
            lines = [
              ...lines,
              {
                ts: parsed.ts ?? "",
                text: parsed.text ?? "",
                lineType: parsed.line_type ?? "",
              },
            ];
          } catch {
            // Skip malformed NDJSON lines
          }
        }
      }
    } catch (err: unknown) {
      if (err instanceof DOMException && err.name === "AbortError") {
        return;
      }
    } finally {
      if (version === requestVersion) {
        streaming = false;
      }
    }
  }

  function stopStreaming(): void {
    if (abortController) {
      abortController.abort();
      abortController = null;
    }
    connectedJobId = undefined;
    streaming = false;
  }

  async function loadSnapshot(jobId: number): Promise<void> {
    stopStreaming();
    const version = ++requestVersion;
    lines = [];
    const { data, error } = await client.GET("/api/job/output", {
      params: { query: { job_id: jobId } },
    });
    if (error || !data) return;
    if (version !== requestVersion) return;
    lines = (data.lines ?? []).map((l) => ({
      ts: l.ts,
      text: l.text,
      lineType: l.line_type,
    }));
  }

  function toggleFollow(): void {
    followMode = !followMode;
  }

  function clear(): void {
    lines = [];
  }

  function getLines(): LogLine[] {
    return lines;
  }
  function isStreaming(): boolean {
    return streaming;
  }
  function getFollowMode(): boolean {
    return followMode;
  }
  function getConnectedJobId(): number | undefined {
    return connectedJobId;
  }

  return {
    getLines,
    isStreaming,
    getFollowMode,
    getConnectedJobId,
    startStreaming,
    stopStreaming,
    loadSnapshot,
    toggleFollow,
    clear,
  };
}

export type LogStore = ReturnType<typeof createLogStore>;
