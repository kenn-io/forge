import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { Effect, Layer } from "effect";
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import type { RepoPresetCatalogEntry } from "../stores/repo-presets.js";
import RepoTeamPicker from "./RepoTeamPickerRuntimeHarness.svelte";
import { installOffsetParentStub, removeOffsetParentStub } from "../../test/stubOffsetParent.js";

beforeAll(installOffsetParentStub);
afterAll(removeOffsetParentStub);

const { listTeams, listTeamRepos } = vi.hoisted(() => ({
  listTeams: vi.fn(),
  listTeamRepos: vi.fn(),
}));

vi.mock("../stores/settings-workflow.js", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../stores/settings-workflow.js")>();
  const { ApiProblemError } = await import("../api/effect-errors.js");
  const failure = (operation: string, cause: unknown) =>
    new ApiProblemError({
      operation,
      problem: {
        code: "upstreamError",
        detail: cause instanceof Error ? cause.message : String(cause),
        type: "about:blank",
      },
    });
  return {
    ...actual,
    SettingsWorkflowLive: Layer.mock(actual.SettingsWorkflow)({
      listGitHubTeams: (host, org) =>
        Effect.tryPromise({ try: () => listTeams(host, org), catch: (cause) => failure("list teams", cause) }),
      listGitHubTeamRepositories: (host, org, slug) =>
        Effect.tryPromise({
          try: () => listTeamRepos(host, org, slug),
          catch: (cause) => failure("list team repositories", cause),
        }),
    }),
  };
});

const catalog: RepoPresetCatalogEntry[] = [
  {
    value: "github|github.com/acme/widgets",
    provider: "github",
    platform_host: "github.com",
    repositoryKey: { kind: "id", id: 1102 },
    repo_path: "acme/widgets",
  },
  {
    value: "github|github.com/acme/tools",
    provider: "github",
    platform_host: "github.com",
    repositoryKey: { kind: "id", id: 1103 },
    repo_path: "acme/tools",
  },
];

const acme = { platform_host: "github.com", owner: "acme" };

function renderPicker(orgs = [acme], catalogReady = true) {
  const onApply = vi.fn();
  const onClose = vi.fn();
  render(RepoTeamPicker, { props: { orgs, catalog, catalogReady, onApply, onClose } });
  return { onApply, onClose };
}

async function chooseTeam(name: string) {
  await fireEvent.click(await screen.findByRole("combobox", { name: /^Team/ }));
  await fireEvent.click(screen.getByRole("option", { name }));
}

describe("RepoTeamPicker", () => {
  beforeEach(() => {
    listTeams.mockReset();
    listTeamRepos.mockReset();
  });

  afterEach(() => {
    cleanup();
  });

  it("selects every configured repository the team lists and hands the selection back", async () => {
    listTeams.mockResolvedValue([{ slug: "payments", name: "Payments" }]);
    listTeamRepos.mockResolvedValue([
      { platform_repo_id: 1102, repo_path: "acme/widgets" },
      { platform_repo_id: 1103, repo_path: "acme/tools" },
    ]);
    const { onApply } = renderPicker();

    await waitFor(() => expect(listTeams).toHaveBeenCalledWith("github.com", "acme"));
    await chooseTeam("Payments");

    expect(listTeamRepos).toHaveBeenCalledWith("github.com", "acme", "payments");
    expect((await screen.findByRole("status")).textContent?.replace(/\s+/g, " ")).toContain(
      "2 configured repositories from Payments will replace the current selection.",
    );
    await fireEvent.click(screen.getByRole("button", { name: "Use selection" }));
    expect(onApply).toHaveBeenCalledWith("github|github.com/acme/widgets,github|github.com/acme/tools");
  });

  it("keeps Use selection disabled when none of the team's repositories are configured", async () => {
    listTeams.mockResolvedValue([{ slug: "payments", name: "Payments" }]);
    listTeamRepos.mockResolvedValue([]);
    renderPicker();

    await chooseTeam("Payments");

    expect((await screen.findByRole("status")).textContent).toContain("in Payments are configured in Forge");
    expect((screen.getByRole("button", { name: "Use selection" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("shows the server's credential failure instead of an empty team list", async () => {
    listTeams.mockRejectedValue(
      new Error("Team lookup needs your own GitHub credential (a personal token or gh login)."),
    );
    renderPicker();

    expect((await screen.findByRole("alert")).textContent).toContain("needs your own GitHub credential");
    expect(screen.queryByText(/No teams in acme/)).toBeNull();
    expect((screen.getByRole("button", { name: "Use selection" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("says so when the organization returns no visible teams", async () => {
    listTeams.mockResolvedValue([]);
    renderPicker();

    expect((await screen.findByRole("status")).textContent).toContain(
      "No teams in acme are visible to this credential.",
    );
  });

  it("waits for an organization choice when several are configured and drops a replaced team reply", async () => {
    const enterprise = { platform_host: "ghe.example.com", owner: "acme" };
    let resolveFirst!: (teams: unknown) => void;
    listTeams
      .mockImplementationOnce(() => new Promise((resolve) => (resolveFirst = resolve)))
      .mockResolvedValueOnce([{ slug: "platform", name: "Platform" }]);
    renderPicker([acme, enterprise]);
    expect(listTeams).not.toHaveBeenCalled();

    const org = screen.getByRole("combobox", { name: /^Organization/ });
    await fireEvent.click(org);
    await fireEvent.click(screen.getByRole("option", { name: "acme (github.com)" }));
    await fireEvent.click(org);
    await fireEvent.click(screen.getByRole("option", { name: "acme (ghe.example.com)" }));
    resolveFirst([{ slug: "payments", name: "Payments" }]);

    await fireEvent.click(await screen.findByRole("combobox", { name: /^Team/ }));
    expect(await screen.findByRole("option", { name: "Platform" })).toBeTruthy();
    expect(screen.queryByRole("option", { name: "Payments" })).toBeNull();
  });
});
