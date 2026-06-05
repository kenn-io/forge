export interface TmuxMouseDragFilterOptions {
  thresholdCells?: number;
}

interface SgrMouseReport {
  raw: string;
  code: number;
  x: number;
  y: number;
  final: "M" | "m";
}

interface PendingDrag {
  startX: number;
  startY: number;
  bufferedReports: string[];
  thresholdExceeded: boolean;
}

const DEFAULT_THRESHOLD_CELLS = 3;
const SGR_MOUSE_PREFIX = "\x1b[<";

export interface TmuxMouseDragFilter {
  filter(data: string): string;
}

export function createTmuxMouseDragFilter(options: TmuxMouseDragFilterOptions = {}): TmuxMouseDragFilter {
  const thresholdCells = options.thresholdCells ?? DEFAULT_THRESHOLD_CELLS;
  let pendingDrag: PendingDrag | null = null;

  function filter(data: string): string {
    const parseResult = parseReports(data);
    let output = "";

    for (const part of parseResult.parts) {
      if (typeof part === "string") {
        output += part;
        continue;
      }
      output += filterReport(part);
    }

    return output;
  }

  function filterReport(report: SgrMouseReport): string {
    if (isLeftButtonDown(report)) {
      pendingDrag = {
        startX: report.x,
        startY: report.y,
        bufferedReports: [],
        thresholdExceeded: false,
      };
      return report.raw;
    }

    if (pendingDrag && isLeftButtonDrag(report)) {
      if (!pendingDrag.thresholdExceeded) {
        pendingDrag.bufferedReports.push(report.raw);
        pendingDrag.thresholdExceeded = dragExceededThreshold(pendingDrag, report);
        if (!pendingDrag.thresholdExceeded) return "";

        const buffered = pendingDrag.bufferedReports.join("");
        pendingDrag.bufferedReports = [];
        return buffered;
      }
      return report.raw;
    }

    if (pendingDrag && isMouseRelease(report)) {
      const output = pendingDrag.thresholdExceeded ? pendingDrag.bufferedReports.join("") + report.raw : report.raw;
      pendingDrag = null;
      return output;
    }

    return report.raw;
  }

  function dragExceededThreshold(drag: PendingDrag, report: SgrMouseReport): boolean {
    return Math.max(Math.abs(report.x - drag.startX), Math.abs(report.y - drag.startY)) > thresholdCells;
  }

  return { filter };
}

function parseReports(input: string): {
  parts: Array<string | SgrMouseReport>;
} {
  const parts: Array<string | SgrMouseReport> = [];
  let cursor = 0;

  while (cursor < input.length) {
    const start = input.indexOf(SGR_MOUSE_PREFIX, cursor);
    if (start === -1) break;

    if (start > cursor) {
      parts.push(input.slice(cursor, start));
    }

    const parsed = parseReportAt(input, start);
    if (!parsed) {
      parts.push(input[start]!);
      cursor = start + 1;
      continue;
    }

    parts.push(parsed.report);
    cursor = parsed.end;
  }

  if (cursor < input.length) {
    parts.push(input.slice(cursor));
  }

  return { parts };
}

function parseReportAt(input: string, start: number): { report: SgrMouseReport; end: number } | null {
  let cursor = start + SGR_MOUSE_PREFIX.length;
  const fields: number[] = [];

  for (let fieldIndex = 0; fieldIndex < 3; fieldIndex++) {
    const fieldStart = cursor;
    while (cursor < input.length && isDigit(input[cursor]!)) cursor++;
    if (cursor === input.length) return null;
    if (cursor === fieldStart) return null;

    fields.push(Number(input.slice(fieldStart, cursor)));

    if (fieldIndex < 2) {
      if (input[cursor] !== ";") return null;
      cursor++;
      continue;
    }

    const final = input[cursor];
    if (final !== "M" && final !== "m") return null;
    cursor++;
    return {
      report: {
        raw: input.slice(start, cursor),
        code: fields[0]!,
        x: fields[1]!,
        y: fields[2]!,
        final,
      },
      end: cursor,
    };
  }

  return null;
}

function isDigit(value: string): boolean {
  return value >= "0" && value <= "9";
}

function isLeftButtonDown(report: SgrMouseReport): boolean {
  return report.final === "M" && !isMotion(report) && !isWheel(report) && button(report) === 0;
}

function isLeftButtonDrag(report: SgrMouseReport): boolean {
  return report.final === "M" && isMotion(report) && !isWheel(report) && button(report) === 0;
}

function isMouseRelease(report: SgrMouseReport): boolean {
  return report.final === "m";
}

function isMotion(report: SgrMouseReport): boolean {
  return (report.code & 32) !== 0;
}

function isWheel(report: SgrMouseReport): boolean {
  return (report.code & 64) !== 0;
}

function button(report: SgrMouseReport): number {
  return report.code & 3;
}
