import type { DiffFile } from "../../api/types.js";
import {
  categorizeDiffFile,
  type DiffFileCategory,
} from "../../utils/diff-categories.js";

export { categorizeDiffFile };
export type DiffSummaryCategory = DiffFileCategory;

export interface DiffLineTotals {
  additions: number;
  deletions: number;
}

export type DiffLineSummary = Record<DiffSummaryCategory | "total", DiffLineTotals>;

export class DiffSummaryFilesResult {
  constructor(
    readonly stale: boolean,
    readonly files: DiffFile[],
  ) {}

  clone(): DiffSummaryFilesResult {
    return new DiffSummaryFilesResult(
      this.stale,
      this.files.map((file) => ({
        ...file,
        hunks: file.hunks.map((hunk) => ({
          ...hunk,
          lines: hunk.lines.map((line) => ({ ...line })),
        })),
      })),
    );
  }
}

const ZERO_TOTALS: DiffLineTotals = { additions: 0, deletions: 0 };

function emptySummary(): DiffLineSummary {
  return {
    plansDocs: { ...ZERO_TOTALS },
    generated: { ...ZERO_TOTALS },
    code: { ...ZERO_TOTALS },
    tests: { ...ZERO_TOTALS },
    other: { ...ZERO_TOTALS },
    total: { ...ZERO_TOTALS },
  };
}

export function summarizeDiffFiles(files: DiffFile[]): DiffLineSummary {
  const summary = emptySummary();
  for (const file of files) {
    const category = categorizeDiffFile(file);
    summary[category].additions += file.additions;
    summary[category].deletions += file.deletions;
    summary.total.additions += file.additions;
    summary.total.deletions += file.deletions;
  }
  return summary;
}
