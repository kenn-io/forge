import type { WorkspaceHost, WorkspaceProject } from "@middleman/ui/api/types";

const project = {
  key: "proj-1",
  name: "test-project",
  kind: "repository",
  repoKind: "STANDARD",
  defaultBranch: "main",
  platformRepo: "acme/test-project",
  platformURL: "https://github.com/acme/test-project",
  worktrees: [],
} satisfies WorkspaceProject;

const host = {
  key: "local",
  label: "Local",
  connectionState: "connected",
  transport: "local",
  platform: "macOS",
  projects: [project],
  sessions: [],
  resources: null,
} satisfies WorkspaceHost;

void host;
