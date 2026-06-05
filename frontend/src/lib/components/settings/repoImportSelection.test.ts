import { describe, expect, it } from "vite-plus/test";
import {
  applyRangeSelection,
  filterRows,
  parseImportPattern,
  rowKey,
  selectedRowsForSubmit,
  setAllVisible,
  sortRows,
  type RepoImportRow,
} from "./repoImportSelection.js";
import { repoImportProvider, repoImportProviders } from "./repoImportProviders.js";

const rows: RepoImportRow[] = [
  {
    provider: "github",
    platform_host: "github.com",
    owner: "acme",
    name: "worker",
    repo_path: "acme/worker",
    description: "Background jobs",
    private: false,
    fork: false,
    pushed_at: "2026-04-20T00:00:00Z",
    already_configured: false,
  },
  {
    provider: "github",
    platform_host: "github.com",
    owner: "acme",
    name: "api",
    repo_path: "acme/api",
    description: "HTTP API",
    private: true,
    fork: false,
    pushed_at: "2026-04-22T00:00:00Z",
    already_configured: false,
  },
  {
    provider: "github",
    platform_host: "github.com",
    owner: "acme",
    name: "empty",
    repo_path: "acme/empty",
    description: null,
    private: false,
    fork: false,
    pushed_at: null,
    already_configured: false,
  },
  {
    provider: "github",
    platform_host: "github.com",
    owner: "acme",
    name: "widget",
    repo_path: "acme/widget",
    description: "Configured",
    private: false,
    fork: true,
    pushed_at: "2026-04-21T00:00:00Z",
    already_configured: true,
  },
];

const githubKey = (name: string) => `github/github.com/acme/${name}`;

describe("repo import selection helpers", () => {
  it("parses owner/pattern and trims whitespace", () => {
    expect(parseImportPattern(" acme / widget-* ")).toEqual({
      owner: "acme",
      pattern: "widget-*",
    });
  });

  it("parses nested namespace patterns when slashes are allowed", () => {
    expect(parseImportPattern(" group/subgroup / project-* ", true)).toEqual({
      owner: "group/subgroup",
      pattern: "project-*",
    });
  });

  it("keeps Forgejo and Gitea owner patterns non-nested", () => {
    expect(repoImportProviders.map((provider) => provider.id)).toEqual(["github", "gitlab", "forgejo", "gitea"]);
    expect(repoImportProvider("forgejo")).toMatchObject({
      defaultHost: "codeberg.org",
      allowNestedOwner: false,
      ownerPatternPlaceholder: "owner/pattern",
    });
    expect(repoImportProvider("gitea")).toMatchObject({
      defaultHost: "gitea.com",
      allowNestedOwner: false,
      ownerPatternPlaceholder: "owner/pattern",
    });
    expect(() => parseImportPattern("team/subgroup/project-*", repoImportProvider("forgejo").allowNestedOwner)).toThrow(
      "Format: owner/pattern",
    );
    expect(() => parseImportPattern("team/subgroup/project-*", repoImportProvider("gitea").allowNestedOwner)).toThrow(
      "Format: owner/pattern",
    );
  });

  it("rejects malformed patterns before the API call", () => {
    expect(() => parseImportPattern("acme/widgets/extra")).toThrow("Format: owner/pattern");
    expect(() => parseImportPattern("acme/widgets/extra", true)).not.toThrow();
    expect(() => parseImportPattern("acme*/widgets")).toThrow("glob syntax in owner is not supported");
    expect(() => parseImportPattern("acme/")).toThrow("pattern is required");
  });

  it("keys rows by provider host and canonical path", () => {
    expect(
      rowKey({
        provider: "gitlab",
        platform_host: "gitlab.example.com",
        owner: "Group/Subgroup",
        name: "Project",
        repo_path: "Group/Subgroup/Project",
      }),
    ).toBe("gitlab/gitlab.example.com/group/subgroup/project");
  });

  it("filters by owner/name, name, description, and status", () => {
    expect(filterRows(rows, "HTTP", "all").map((row) => row.name)).toEqual(["api"]);
    expect(filterRows(rows, "acme/worker", "all").map((row) => row.name)).toEqual(["worker"]);
    expect(filterRows(rows, "", "already-added").map((row) => row.name)).toEqual(["widget"]);
    const selected = new Set([rowKey(rows[0]!)]);
    expect(filterRows(rows, "", "selected", selected).map((row) => row.name)).toEqual(["worker"]);
    expect(filterRows(rows, "", "unselected", selected).map((row) => row.name)).toEqual(["api", "empty"]);
  });

  it("filters private repositories and forks independently", () => {
    expect(filterRows(rows, "", "all", new Set(), { hidePrivate: true }).map((row) => row.name)).toEqual([
      "worker",
      "empty",
      "widget",
    ]);
    expect(filterRows(rows, "", "all", new Set(), { hideForks: true }).map((row) => row.name)).toEqual([
      "worker",
      "api",
      "empty",
    ]);
    expect(
      filterRows(rows, "", "all", new Set(), {
        hidePrivate: true,
        hideForks: true,
      }).map((row) => row.name),
    ).toEqual(["worker", "empty"]);
  });

  it("sorts deterministically with null pushed_at last", () => {
    expect(sortRows(rows, { field: "pushed_at", direction: "desc" }).map((row) => row.name)).toEqual([
      "api",
      "widget",
      "worker",
      "empty",
    ]);
    expect(sortRows(rows, { field: "pushed_at", direction: "asc" }).map((row) => row.name)).toEqual([
      "worker",
      "widget",
      "api",
      "empty",
    ]);
    expect(sortRows(rows, { field: "name", direction: "asc" }).map((row) => row.name)).toEqual([
      "api",
      "empty",
      "widget",
      "worker",
    ]);
  });

  it("selects and deselects all visible selectable rows", () => {
    const selected = setAllVisible(new Set<string>(), rows, true);
    expect([...selected].sort()).toEqual([githubKey("api"), githubKey("empty"), githubKey("worker")]);
    expect([...setAllVisible(selected, [rows[0]!, rows[3]!], false)].sort()).toEqual([
      githubKey("api"),
      githubKey("empty"),
    ]);
  });

  it("applies shift-click ranges with visible anchors", () => {
    const visible = sortRows(rows, { field: "name", direction: "asc" });
    const result = applyRangeSelection({
      selected: new Set<string>(),
      visibleRows: visible,
      anchorKey: githubKey("api"),
      clickedKey: githubKey("worker"),
      checked: true,
    });
    expect([...result.selected].sort()).toEqual([githubKey("api"), githubKey("empty"), githubKey("worker")]);
    expect(result.anchorKey).toBe(githubKey("worker"));
  });

  it("treats hidden anchors as normal clicks", () => {
    const result = applyRangeSelection({
      selected: new Set<string>(),
      visibleRows: [rows[1]!],
      anchorKey: githubKey("worker"),
      clickedKey: githubKey("api"),
      checked: true,
    });
    expect([...result.selected]).toEqual([githubKey("api")]);
    expect(result.anchorKey).toBe(githubKey("api"));
  });

  it("returns selected rows for submit in full sorted order", () => {
    const sorted = sortRows(rows, { field: "name", direction: "asc" });
    const selected = new Set([githubKey("worker"), githubKey("api")]);
    expect(selectedRowsForSubmit(sorted, selected).map((row) => row.name)).toEqual(["api", "worker"]);
    expect(
      selectedRowsForSubmit(sorted, selected, {
        hidePrivate: true,
      }).map((row) => row.name),
    ).toEqual(["worker"]);
  });
});
