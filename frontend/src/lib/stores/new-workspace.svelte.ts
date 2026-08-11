// Open state for the "new workspace" dialog. The dialog is mounted once at the
// app shell so both entry points — the Workspaces sidebar button and the
// command palette, which can fire from any page — drive the same instance.

export type NewWorkspaceRepoSeed = {
  provider: string;
  platformHost: string;
  owner: string;
  name: string;
};

export type NewWorkspaceSource = "repository" | "kata_issue";

// Repo of the last workspace started from the dialog. Ad-hoc work tends to
// repeat in the same repository, so the picker prefers it whenever the caller
// did not supply a more specific seed.
const LAST_REPO_STORAGE_KEY = "kenn-forge:workspace:new_repo";

let open = $state(false);
let seedRepo = $state<NewWorkspaceRepoSeed | null>(null);
let source = $state<NewWorkspaceSource>("repository");

export function openNewWorkspaceDialog(
  repo?: NewWorkspaceRepoSeed,
  initialSource: NewWorkspaceSource = "repository",
): void {
  seedRepo = repo ?? null;
  source = initialSource;
  open = true;
}

export function closeNewWorkspaceDialog(): void {
  open = false;
  seedRepo = null;
  source = "repository";
}

export function isNewWorkspaceDialogOpen(): boolean {
  return open;
}

// Repository to preselect, when the caller knew one (e.g. the workspace
// currently open in the sidebar).
export function getNewWorkspaceSeedRepo(): NewWorkspaceRepoSeed | null {
  return seedRepo;
}

export function getNewWorkspaceSource(): NewWorkspaceSource {
  return source;
}

// Repository key (`provider/host/owner/name`) of the last created workspace,
// or an empty string when nothing was created on this machine yet.
export function getLastUsedNewWorkspaceRepoKey(): string {
  try {
    return localStorage.getItem(LAST_REPO_STORAGE_KEY) ?? "";
  } catch {
    return "";
  }
}

export function rememberNewWorkspaceRepoKey(key: string): void {
  try {
    if (key) localStorage.setItem(LAST_REPO_STORAGE_KEY, key);
    else localStorage.removeItem(LAST_REPO_STORAGE_KEY);
  } catch {
    // Storage can be disabled; the next open just falls back to the first repo.
  }
}

export function resetNewWorkspaceDialogState(): void {
  open = false;
  seedRepo = null;
  source = "repository";
}
