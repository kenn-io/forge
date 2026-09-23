package authapi

// hostRuntimeScope is the runtime manager scope for sessions launched at
// host level, i.e. not tied to a registered project worktree. Workspace IDs
// are prefixed ("ws_") and worktree scopes use "project-worktree:", so the
// bare name cannot collide.
const HostRuntimeScope = "host"
