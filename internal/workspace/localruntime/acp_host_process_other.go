//go:build !linux && !darwin

package localruntime

// ACPHostsCanShareAgents reports whether a shared host can list what its
// agent runs, so binding a chat notices a replaced agent. Sharing needs it.
const ACPHostsCanShareAgents = false

// acpProcessTree lists nothing where Forge cannot read another process's
// executable and arguments.
func acpProcessTree(int) []agentProcess { return nil }
