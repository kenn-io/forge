package gitclone

import "strings"

// FailureReason classifies Git's error output for sync telemetry. An empty
// reason means the output doesn't identify a remote or network failure.
func FailureReason(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "returned error: 429"):
		return "rate_limited"
	case isAuthGitError(err), strings.Contains(msg, "returned error: 401"), strings.Contains(msg, "returned error: 403"):
		return "auth"
	case strings.Contains(msg, "returned error: 404"), strings.Contains(msg, "returned error: 410"):
		return "not_found"
	case strings.Contains(msg, "fatal: repository '") && strings.Contains(msg, "' not found"):
		return "not_found"
	case strings.Contains(msg, "internal server error"), strings.Contains(msg, "returned error: 5"):
		return "provider"
	}
	for _, needle := range transientGitErrorSubstrings {
		if strings.Contains(msg, needle) {
			return "network"
		}
	}
	if strings.Contains(msg, "unable to access 'http://") || strings.Contains(msg, "unable to access 'https://") || strings.Contains(msg, "rpc failed; curl ") || strings.Contains(msg, "certificate verification failed") || strings.Contains(msg, "ssl certificate problem") {
		return "network"
	}
	return ""
}
