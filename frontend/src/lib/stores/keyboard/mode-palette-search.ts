import type { DocsAPI } from "../../api/docs/api.js";
import type { BodySnippet } from "../../api/docs/types.js";
import type { KataTaskSummary } from "../../api/kata/taskTypes.js";
import type {
  KataAuxiliaryAuthoritySource,
  KataAuxiliaryIssue,
} from "../../features/kata/kataAuxiliaryAuthority.svelte.js";

export const MODE_SEARCH_DISPLAY_LIMIT = 10;

export interface ModeTaskResult {
  kind: "kata-task";
  uid: string;
  short_id: string;
  qualified_id: string;
  title: string;
  project_uid: string;
  project_name: string;
  status: KataTaskSummary["status"];
  // The daemon that served this search hit. Task UIDs are only unique
  // per daemon, so opening a result must target the same daemon.
  daemon_id?: string | undefined;
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
  kata: Pick<KataAuxiliaryAuthoritySource, "issues" | "daemonID">;
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
  const docsPromise = searchDocs(trimmed, deps.docs, limit);
  const tasks = searchTasks(trimmed, deps.kata);
  const docs = await docsPromise;
  return { query: trimmed, tasks, docs };
}

function taskRowFromIssue(issue: KataAuxiliaryIssue, daemonId: string | undefined): ModeTaskResult {
  return {
    kind: "kata-task",
    uid: issue.uid,
    short_id: issue.short_id,
    qualified_id: issue.qualified_id,
    title: issue.title,
    project_uid: issue.project_uid,
    project_name: issue.project_name,
    status: issue.status,
    daemon_id: daemonId,
  };
}

function searchTasks(
  query: string,
  kata: Pick<KataAuxiliaryAuthoritySource, "issues" | "daemonID">,
): ModeSectionResult<ModeTaskResult> {
  const needle = query.toLocaleLowerCase();
  const rows = kata.issues
    .map((issue) => ({ issue, rank: taskSearchRank(issue, needle) }))
    .filter((candidate) => candidate.rank !== null)
    .sort((left, right) => left.rank! - right.rank! || left.issue.qualified_id.localeCompare(right.issue.qualified_id))
    .map(({ issue }) => taskRowFromIssue(issue, kata.daemonID));
  const truncated = rows.length > MODE_SEARCH_DISPLAY_LIMIT;
  return {
    ok: true,
    rows: truncated ? rows.slice(0, MODE_SEARCH_DISPLAY_LIMIT) : rows,
    truncated,
  };
}

function taskSearchRank(issue: KataAuxiliaryIssue, needle: string): number | null {
  const shortID = issue.short_id.toLocaleLowerCase();
  const qualifiedID = issue.qualified_id.toLocaleLowerCase();
  const title = issue.title.toLocaleLowerCase();
  if (shortID === needle || qualifiedID === needle) return 0;
  if (title.startsWith(needle)) return 1;
  if (title.includes(needle)) return 2;
  if (shortID.includes(needle) || qualifiedID.includes(needle)) return 3;
  const searchable = [issue.body, issue.project_name, issue.owner, ...(issue.labels ?? [])];
  return searchable.some((value) => value?.toLocaleLowerCase().includes(needle)) ? 4 : null;
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
