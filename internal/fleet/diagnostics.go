package fleet

// Operation names used in HostDiagnostic.BlocksOperations.
const (
	OpWorktreeCreate    = "worktreeCreate"
	OpPullRequestImport = "pullRequestImport"
	OpDurableSessions   = "durableSessions"
)

// DiagnosticsFromCapabilities computes dependency-based
// diagnostics from host capabilities. Each diagnostic
// carries the operation names it blocks.
func DiagnosticsFromCapabilities(
	caps Capabilities,
) []HostDiagnostic {
	var out []HostDiagnostic

	if !caps.Dependencies.Git {
		out = append(out, HostDiagnostic{
			Code:               "missingGit",
			Severity:           "error",
			Summary:            "Missing git",
			RecoverySuggestion: "Install git on the host.",
			BlocksOperations: []string{
				OpWorktreeCreate,
				OpPullRequestImport,
			},
		})
	}

	if !caps.Dependencies.Gh {
		out = append(out, HostDiagnostic{
			Code:     "missingGh",
			Severity: "warning",
			Summary:  "Missing gh",
			RecoverySuggestion: "Install GitHub CLI (gh) on" +
				" the host to import pull requests.",
			BlocksOperations: []string{
				OpPullRequestImport,
			},
		})
	}

	if !caps.Dependencies.Tmux {
		out = append(out, HostDiagnostic{
			Code:               "missingTmux",
			Severity:           "warning",
			Summary:            "Missing tmux",
			RecoverySuggestion: "Install tmux on the host.",
			BlocksOperations: []string{
				OpDurableSessions,
			},
		})
	}

	return out
}
