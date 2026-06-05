/**
 * Global workspace type declarations for the @middleman/ui package.
 *
 * These mirror the types in frontend/src/vite-env.d.ts so that
 * Svelte components in packages/ui can reference them without
 * explicit imports.
 */

// import.meta.env shape — packages/ui does not depend on vite directly,
// so the bundled `vite/client` types aren't resolvable from this tsconfig.
// Declare the subset we actually read at runtime. `DEV` is the only field
// utils/ci-buckets-warn.ts inspects today; broaden as needed.
interface ImportMetaEnv {
  readonly DEV?: boolean;
  readonly MODE?: string;
  readonly PROD?: boolean;
  readonly SSR?: boolean;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}

interface WorkspaceHost {
  key: string;
  label: string;
  connectionState: "connected" | "connecting" | "disconnected" | "error";
  transport?: "ssh" | "local";
  platform?: string;
  projects: WorkspaceProject[];
  sessions: WorkspaceSession[];
  resources: WorkspaceResources | null;
}

interface WorkspaceProject {
  key: string;
  name: string;
  kind: "repository" | "scratch";
  repoKind: string;
  defaultBranch: string;
  platformRepo: string | null;
  platformURL?: string;
  worktrees: WorkspaceWorktree[];
}

interface WorkspaceWorktree {
  key: string;
  name: string;
  branch: string;
  isPrimary: boolean;
  isHidden: boolean;
  isStale: boolean;
  sessionBackend: string | null;
  linkedPR: WorkspaceLinkedPR | null;
  activity: WorkspaceActivity;
  diff: WorkspaceDiff | null;
}

interface WorkspaceLinkedPR {
  number: number;
  title: string;
  state: "open" | "closed" | "merged";
  checksStatus: string | null;
  updatedAt: string | null;
}

interface WorkspaceActivity {
  state: "idle" | "active" | "running" | "needsAttention";
  lastOutputAt: string | null;
}

interface WorkspaceDiff {
  added: number;
  removed: number;
}

interface WorkspaceSession {
  key: string;
  name: string;
  worktreeKey: string | null;
  isHidden: boolean;
}

interface WorkspaceResources {
  cpuPercent: number;
  residentMB: number;
}

interface WorkspaceData {
  hosts: WorkspaceHost[];
  selectedWorktreeKey: string | null;
  selectedHostKey: string | null;
}

interface CommandResult {
  ok: boolean;
  message?: string;
}

interface WorkspaceCommandHandler {
  (command: string, payload: Record<string, unknown>): CommandResult | Promise<CommandResult>;
}

interface WorkspaceDetailContext {
  worktree: WorkspaceWorktree | null;
  project: WorkspaceProject | null;
  host: WorkspaceHost | null;
}
