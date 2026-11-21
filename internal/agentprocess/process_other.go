//go:build !linux && !darwin

package agentprocess

// Other platforms cannot read process identity, so a session there ends only
// through its SessionEnd hook.

func readProcessState(int) (processInfo, bool, bool) { return processInfo{}, false, false }
