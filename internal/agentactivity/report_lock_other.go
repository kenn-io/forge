//go:build !windows

package agentactivity

func isReportLockError(error) bool {
	return false
}
