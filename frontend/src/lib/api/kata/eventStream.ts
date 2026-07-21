import { KATA_DAEMON_HEADER } from "./daemons.js";

interface FrameState {
  id?: number;
  event?: string;
  data: string[];
}

export interface ReadKataEventStreamOptions {
  daemonId?: string | undefined;
  fetchImpl?: typeof fetch | undefined;
  lastEventID?: number | undefined;
  signal?: AbortSignal | undefined;
  onOpen?: (() => void) | undefined;
  onMessage(message: KataTaskEventStreamFrame): void | Promise<void>;
}

export interface KataTaskEventStreamFrame {
  kind: "reset" | "invalidation";
  server_instance_id: string;
  daemon_id: string;
  epoch: number;
  cursor: number;
}

export class KataEventStreamError extends Error {
  readonly retryable: boolean;

  constructor(message: string, options: { retryable: boolean }) {
    super(message);
    this.name = "KataEventStreamError";
    this.retryable = options.retryable;
  }
}

function isRetryableStreamSetupStatus(status: number): boolean {
  return status === 408 || status === 429 || status >= 500;
}

function isObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function nonNegativeSafeInteger(value: unknown): number | undefined {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0 ? value : undefined;
}

function nonEmptyString(value: unknown): string | undefined {
  return typeof value === "string" && value.length > 0 ? value : undefined;
}

function parseData(data: string): Record<string, unknown> | undefined {
  try {
    const parsed: unknown = JSON.parse(data);
    return isObject(parsed) ? parsed : undefined;
  } catch {
    return undefined;
  }
}

export class KataEventStreamParser {
  private buffer = "";
  private frame: FrameState = { data: [] };

  push(chunk: string): KataTaskEventStreamFrame[] {
    this.buffer += chunk;
    const messages: KataTaskEventStreamFrame[] = [];

    for (;;) {
      const newline = this.buffer.indexOf("\n");
      if (newline === -1) break;
      const rawLine = this.buffer.slice(0, newline);
      this.buffer = this.buffer.slice(newline + 1);
      const line = rawLine.endsWith("\r") ? rawLine.slice(0, -1) : rawLine;
      const message = this.consumeLine(line);
      if (message) messages.push(message);
    }

    return messages;
  }

  flush(): KataTaskEventStreamFrame[] {
    const messages: KataTaskEventStreamFrame[] = [];
    if (this.buffer.length > 0) {
      const line = this.buffer.endsWith("\r") ? this.buffer.slice(0, -1) : this.buffer;
      this.buffer = "";
      const message = this.consumeLine(line);
      if (message) messages.push(message);
    }
    const message = this.commitFrame();
    if (message) messages.push(message);
    return messages;
  }

  private consumeLine(line: string): KataTaskEventStreamFrame | undefined {
    if (line === "") {
      return this.commitFrame();
    }
    if (line.startsWith(":")) {
      return undefined;
    }

    const colon = line.indexOf(":");
    const field = colon === -1 ? line : line.slice(0, colon);
    const rawValue = colon === -1 ? "" : line.slice(colon + 1);
    const value = rawValue.startsWith(" ") ? rawValue.slice(1) : rawValue;

    switch (field) {
      case "id": {
        const id = Number(value);
        if (Number.isSafeInteger(id) && id >= 0) this.frame.id = id;
        break;
      }
      case "event":
        this.frame.event = value;
        break;
      case "data":
        this.frame.data.push(value);
        break;
      default:
        break;
    }
    return undefined;
  }

  private commitFrame(): KataTaskEventStreamFrame | undefined {
    const frame = this.frame;
    this.frame = { data: [] };
    if (frame.id === undefined || frame.data.length === 0) return undefined;
    let kind: KataTaskEventStreamFrame["kind"];
    if (frame.event === "kata.tasks.reset") {
      kind = "reset";
    } else if (frame.event === "kata.tasks.invalidated") {
      kind = "invalidation";
    } else {
      return undefined;
    }

    const body = parseData(frame.data.join("\n"));
    if (!body) return undefined;
    const serverInstanceID = nonEmptyString(body.server_instance_id);
    const daemonID = nonEmptyString(body.daemon_id);
    const epoch = nonNegativeSafeInteger(body.epoch);
    const cursor = nonNegativeSafeInteger(body.cursor);
    if (serverInstanceID === undefined || daemonID === undefined || epoch === undefined || cursor !== frame.id) {
      return undefined;
    }

    return {
      kind,
      server_instance_id: serverInstanceID,
      daemon_id: daemonID,
      epoch,
      cursor,
    };
  }
}

export async function readKataEventStream(options: ReadKataEventStreamOptions): Promise<void> {
  const fetchImpl = options.fetchImpl ?? fetch;

  const headers = new Headers({ Accept: "text/event-stream" });
  if (options.daemonId) {
    headers.set(KATA_DAEMON_HEADER, options.daemonId);
  }
  if (options.lastEventID && options.lastEventID > 0) {
    headers.set("Last-Event-ID", String(options.lastEventID));
  }

  let response: Response;
  try {
    const init: RequestInit = { headers };
    if (options.signal) {
      init.signal = options.signal;
    }
    response = await fetchImpl("/api/v1/kata/tasks/events", init);
  } catch (error) {
    if (options.signal?.aborted) return;
    throw error;
  }

  if (!response.ok) {
    throw new KataEventStreamError(`Kata event stream failed: HTTP ${response.status}`, {
      retryable: isRetryableStreamSetupStatus(response.status),
    });
  }
  if (!response.body) {
    throw new KataEventStreamError("Kata event stream response has no body", { retryable: false });
  }
  options.onOpen?.();

  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  const parser = new KataEventStreamParser();
  const abortReader = () => {
    void reader.cancel();
  };
  options.signal?.addEventListener("abort", abortReader, { once: true });

  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) {
        const tail = decoder.decode();
        const messages = [...(tail ? parser.push(tail) : []), ...parser.flush()];
        for (const message of messages) {
          await options.onMessage(message);
        }
        break;
      }
      const messages = parser.push(decoder.decode(value, { stream: true }));
      for (const message of messages) {
        await options.onMessage(message);
      }
    }
    throw new KataEventStreamError("Live updates disconnected", { retryable: true });
  } catch (error) {
    if (options.signal?.aborted) return;
    throw error;
  } finally {
    options.signal?.removeEventListener("abort", abortReader);
    reader.releaseLock();
  }
}
