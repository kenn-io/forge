import type { DocsAPI } from "../../api/docs/api.js";
import type { BodySnippet } from "../../api/docs/types.js";
import type { KataTaskAPI, KataTaskSummary } from "../../api/kata/taskTypes.js";

export const MODE_SEARCH_DISPLAY_LIMIT = 10;

export interface ModeTaskResult {
  kind: "kata-task";
  uid: string;
  short_id: string;
  qualified_id: string;
  title: string;
  project_name: string;
  status: KataTaskSummary["status"];
}

export interface ModeDocResult {
  kind: "doc";
  folder: string;
  folder_name: string;
  rel_path: string;
  hit_type: "filename" | "body";
  line?: number | undefined;
  snippet?: BodySnippet | undefined;
}

export type ModePaletteRow = ModeTaskResult | ModeDocResult;

export type ModeSectionResult<R> = { ok: true; rows: R[]; truncated: boolean } | { ok: false; error: string };

export type ModeDocsSectionResult =
  | { ok: true; rows: ModeDocResult[]; truncated: boolean; warnings?: string[] | undefined }
  | { ok: false; error: string };

export interface ModePaletteResults {
  query: string;
  tasks: ModeSectionResult<ModeTaskResult>;
  docs: ModeDocsSectionResult;
}

export interface ModePaletteSearchDeps {
  kata: Pick<KataTaskAPI, "search">;
  docs: Pick<DocsAPI, "searchAll">;
}

export async function searchModePalette(query: string, deps: ModePaletteSearchDeps): Promise<ModePaletteResults> {
  const trimmed = query.trim();
  if (!trimmed) {
    return {
      query,
      tasks: { ok: true, rows: [], truncated: false },
      docs: { ok: true, rows: [], truncated: false },
    };
  }

  const limit = MODE_SEARCH_DISPLAY_LIMIT + 1;
  const [tasks, docs] = await Promise.all([searchTasks(trimmed, deps.kata), searchDocs(trimmed, deps.docs, limit)]);
  return { query: trimmed, tasks, docs };
}

function taskRowFromIssue(issue: KataTaskSummary): ModeTaskResult {
  return {
    kind: "kata-task",
    uid: issue.uid,
    short_id: issue.short_id,
    qualified_id: issue.qualified_id,
    title: issue.title,
    project_name: issue.project_name,
    status: issue.status,
  };
}

async function searchTasks(
  query: string,
  kata: Pick<KataTaskAPI, "search">,
): Promise<ModeSectionResult<ModeTaskResult>> {
  try {
    const response = await kata.search({
      scope: { kind: "all" },
      status: "all",
      owner: "",
      label: "",
      query,
    });
    const rows = response.issues.map(taskRowFromIssue);
    const truncated = rows.length > MODE_SEARCH_DISPLAY_LIMIT;
    return {
      ok: true,
      rows: truncated ? rows.slice(0, MODE_SEARCH_DISPLAY_LIMIT) : rows,
      truncated,
    };
  } catch (err) {
    return { ok: false, error: err instanceof Error ? err.message : String(err) };
  }
}

async function searchDocs(
  query: string,
  docs: Pick<DocsAPI, "searchAll">,
  limit: number,
): Promise<ModeDocsSectionResult> {
  try {
    const response = await docs.searchAll(query, limit);
    const rows = response.hits.map<ModeDocResult>((hit) => ({
      kind: "doc",
      folder: hit.folder,
      folder_name: hit.folder_name,
      rel_path: hit.rel_path,
      hit_type: hit.hit_type,
      ...(hit.line !== undefined ? { line: hit.line } : {}),
      ...(hit.snippet !== undefined ? { snippet: hit.snippet } : {}),
    }));
    const truncated = response.truncated || rows.length > MODE_SEARCH_DISPLAY_LIMIT;
    return {
      ok: true,
      rows: truncated ? rows.slice(0, MODE_SEARCH_DISPLAY_LIMIT) : rows,
      truncated,
      ...(response.warnings !== undefined ? { warnings: response.warnings } : {}),
    };
  } catch (err) {
    return { ok: false, error: err instanceof Error ? err.message : String(err) };
  }
}
