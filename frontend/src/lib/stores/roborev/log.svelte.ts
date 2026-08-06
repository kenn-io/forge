import { Effect, Stream } from "effect";
import type { AppRuntime } from "../../app/runtime.js";
import { loadRoborevJobOutput, roborevJobOutputStream } from "../../api/roborev/client.js";
import type { RoborevLogLinePayload } from "../../api/roborev/schemas.js";
import { makeRoborevOwner, RoborevWorkflow } from "./roborev-workflow.js";

interface LogLine {
  ts: string;
  text: string;
  lineType: string;
}

export interface LogStoreOptions {
  runtime: AppRuntime;
  baseUrl: string;
  onError?: (message: string) => void;
}

function logLineFromPayload(payload: RoborevLogLinePayload): LogLine {
  return {
    ts: payload.ts ?? "",
    text: payload.text ?? "",
    lineType: payload.line_type ?? "",
  };
}

export function createLogStore(opts: LogStoreOptions) {
  let lines = $state.raw<LogLine[]>([]);
  let streaming = $state(false);
  let followMode = $state(true);
  let connectedJobId = $state<number | undefined>(undefined);
  let activeLogOwner: string | undefined;

  const startStreamingEffect = (
    jobId: number,
    logOwner: string,
    claimedPreviousOwner?: string,
    requirePublicClaim = false,
  ) =>
    Effect.gen(function* () {
      const workflow = yield* RoborevWorkflow;
      if (requirePublicClaim && activeLogOwner !== logOwner) return;
      const previousOwner = claimedPreviousOwner ?? activeLogOwner;
      yield* Effect.sync(() => {
        activeLogOwner = logOwner;
        connectedJobId = jobId;
        streaming = true;
        lines = [];
      });
      if (previousOwner !== undefined && previousOwner !== logOwner) yield* workflow.stopLog(previousOwner);
      yield* workflow.log(
        logOwner,
        jobId,
        Stream.runForEach(roborevJobOutputStream(opts.baseUrl, jobId), (payload) =>
          Effect.sync(() => {
            if (activeLogOwner !== logOwner) return;
            lines = [...lines, logLineFromPayload(payload)];
          }),
        ).pipe(
          Effect.catch((failure) =>
            Effect.sync(() => {
              if (activeLogOwner !== logOwner) return;
              opts.onError?.(failure.message);
            }),
          ),
          Effect.ensuring(
            Effect.sync(() => {
              if (activeLogOwner !== logOwner) return;
              streaming = false;
            }),
          ),
        ),
      );
    });

  function startStreaming(jobId: number): string {
    const logOwner = makeRoborevOwner("roborev-log");
    const previousOwner = activeLogOwner;
    activeLogOwner = logOwner;
    connectedJobId = jobId;
    streaming = true;
    lines = [];
    opts.runtime.runCommand(startStreamingEffect(jobId, logOwner, previousOwner, true), {
      operation: "stream Roborev job output",
      safeContext: { job_id: jobId, owner: logOwner },
      onFailure: () => {},
    });
    return logOwner;
  }

  const stopStreamingEffect = (logOwner: string) =>
    Effect.gen(function* () {
      const workflow = yield* RoborevWorkflow;
      yield* workflow.stopLog(logOwner);
      yield* Effect.sync(() => {
        if (activeLogOwner !== logOwner) return;
        activeLogOwner = undefined;
        connectedJobId = undefined;
        streaming = false;
      });
    });

  function stopStreaming(logOwner: string): void {
    opts.runtime.runCommand(stopStreamingEffect(logOwner), {
      operation: "stop Roborev job output",
      safeContext: { owner: logOwner },
      onFailure: () => {},
    });
  }

  const loadSnapshotEffect = (
    jobId: number,
    logOwner: string,
    claimedPreviousOwner?: string,
    requirePublicClaim = false,
  ) =>
    Effect.gen(function* () {
      const workflow = yield* RoborevWorkflow;
      if (requirePublicClaim && activeLogOwner !== logOwner) return;
      const previousOwner = claimedPreviousOwner ?? activeLogOwner;
      yield* Effect.sync(() => {
        activeLogOwner = logOwner;
        connectedJobId = undefined;
        streaming = false;
        lines = [];
      });
      if (previousOwner !== undefined && previousOwner !== logOwner) yield* workflow.stopLog(previousOwner);
      yield* workflow.log(
        logOwner,
        jobId,
        loadRoborevJobOutput(opts.baseUrl, jobId).pipe(
          Effect.tap((snapshot) =>
            Effect.sync(() => {
              if (activeLogOwner !== logOwner) return;
              lines = (snapshot.lines ?? []).map(logLineFromPayload);
            }),
          ),
          Effect.catch((failure) =>
            Effect.sync(() => {
              if (activeLogOwner !== logOwner) return;
              opts.onError?.(failure.message);
            }),
          ),
        ),
      );
    });

  function loadSnapshot(jobId: number): string {
    const logOwner = makeRoborevOwner("roborev-log");
    const previousOwner = activeLogOwner;
    activeLogOwner = logOwner;
    connectedJobId = undefined;
    streaming = false;
    lines = [];
    opts.runtime.runCommand(loadSnapshotEffect(jobId, logOwner, previousOwner, true), {
      operation: "load Roborev job output",
      safeContext: { job_id: jobId, owner: logOwner },
      onFailure: () => {},
    });
    return logOwner;
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
    startStreamingEffect,
    stopStreaming,
    stopStreamingEffect,
    loadSnapshot,
    loadSnapshotEffect,
    toggleFollow,
    clear,
  };
}

export type LogStore = ReturnType<typeof createLogStore>;
