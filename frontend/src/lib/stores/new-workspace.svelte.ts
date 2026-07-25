// Open state for the "new workspace" dialog. The dialog is mounted once at the
// app shell so both entry points — the Workspaces sidebar button and the
// command palette, which can fire from any page — drive the same instance.

export type NewWorkspaceRepoSeed = {
  provider: string;
  platformHost: string;
  owner: string;
  name: string;
};

let open = $state(false);
let seedRepo = $state<NewWorkspaceRepoSeed | null>(null);

export function openNewWorkspaceDialog(repo?: NewWorkspaceRepoSeed): void {
  seedRepo = repo ?? null;
  open = true;
}

export function closeNewWorkspaceDialog(): void {
  open = false;
  seedRepo = null;
}

export function isNewWorkspaceDialogOpen(): boolean {
  return open;
}

// Repository to preselect, when the caller knew one (e.g. the workspace
// currently open in the sidebar).
export function getNewWorkspaceSeedRepo(): NewWorkspaceRepoSeed | null {
  return seedRepo;
}

export function resetNewWorkspaceDialogState(): void {
  open = false;
  seedRepo = null;
}
