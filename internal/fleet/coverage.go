package fleet

const defaultPlatformHost = "github.com"

// effectiveHost returns the platform host for a project,
// defaulting to "github.com" when empty.
func effectiveHost(host string) string {
	if host == "" {
		return defaultPlatformHost
	}
	return host
}

// ActivePlatformHost selects the platform host with the most
// projects. Tie-break: prefer "github.com", then lexicographic
// order. Returns nil when no projects have platform repos.
func ActivePlatformHost(projects []RawProject) *string {
	counts := make(map[string]int)
	for _, p := range projects {
		if p.PlatformRepo == "" {
			continue
		}
		counts[effectiveHost(p.PlatformHost)]++
	}

	if len(counts) == 0 {
		return nil
	}

	best := ""
	bestCount := 0
	for host, count := range counts {
		if count > bestCount {
			best = host
			bestCount = count
		} else if count == bestCount {
			if host == defaultPlatformHost {
				best = host
			} else if best != defaultPlatformHost && host < best {
				best = host
			}
		}
	}

	return &best
}

// PlatformCoverage returns the coverage status for a project relative to the
// active platform host: nil (no platform repo / no active host), "unsupported"
// (different host), "degraded" (host matches but the backend reports not
// ready), or "active". BackendReady is a generic readiness signal: nil means
// unknown and is treated as ready.
func PlatformCoverage(p RawProject, activeHost *string) *string {
	if p.PlatformRepo == "" || activeHost == nil {
		return nil
	}
	if effectiveHost(p.PlatformHost) != *activeHost {
		s := "unsupported"
		return &s
	}
	if p.BackendReady != nil && !*p.BackendReady {
		s := "degraded"
		return &s
	}
	s := "active"
	return &s
}
