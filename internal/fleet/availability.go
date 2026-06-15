package fleet

// Operation keys for the availability map.
const (
	OpWorktreeDelete  = "worktreeDelete"
	OpSessionEnsure   = "sessionEnsure"
	OpSessionKill     = "sessionKill"
	OpRepositoryClone = "repositoryClone"
	OpProjectAdd      = "projectAdd"
	OpProjectRemove   = "projectRemove"
)

// operationDef maps an operation key to its
// CommandCapabilities field check and fallback reason.
type operationDef struct {
	capCheck func(CommandCapabilities) bool
	reason   string
}

var operationDefs = map[string]operationDef{
	OpWorktreeCreate: {
		capCheck: func(c CommandCapabilities) bool {
			return c.WorktreeCreate
		},
		reason: "Worktree creation is currently unavailable.",
	},
	OpPullRequestImport: {
		capCheck: func(c CommandCapabilities) bool {
			return c.WorktreeImportPR
		},
		reason: "Pull request import is currently unavailable.",
	},
	OpWorktreeDelete: {
		capCheck: func(c CommandCapabilities) bool {
			return c.WorktreeDelete
		},
		reason: "Worktree deletion is currently unavailable.",
	},
	OpSessionEnsure: {
		capCheck: func(c CommandCapabilities) bool {
			return c.SessionEnsure
		},
		reason: "Session creation is currently unavailable.",
	},
	OpSessionKill: {
		capCheck: func(c CommandCapabilities) bool {
			return c.SessionKill
		},
		reason: "Session termination is currently unavailable.",
	},
	OpDurableSessions: {
		capCheck: func(c CommandCapabilities) bool {
			return c.SessionEnsure
		},
		reason: "Durable sessions are currently unavailable.",
	},
	OpRepositoryClone: {
		capCheck: func(c CommandCapabilities) bool {
			return c.RepositoryClone
		},
		reason: "Repository cloning is currently unavailable.",
	},
	OpProjectAdd: {
		capCheck: func(c CommandCapabilities) bool {
			return c.ProjectAdd
		},
		reason: "Adding projects is currently unavailable.",
	},
	OpProjectRemove: {
		capCheck: func(c CommandCapabilities) bool {
			return c.ProjectRemove
		},
		reason: "Removing projects is currently unavailable.",
	},
}

func unavailable(
	reason string,
) HostOperationAvailability {
	return HostOperationAvailability{
		Available:         false,
		UnavailableReason: &reason,
	}
}

// AvailabilityPolicy adjusts the capability-derived availability map after
// per-operation capability checks. It lets one enrichment serve both a local
// daemon that performs operations and a read-only hub that does not route
// writes. Apply mutates result in place; reachable reports host reachability.
type AvailabilityPolicy interface {
	Apply(result map[string]HostOperationAvailability, reachable bool)
}

// RealCapabilityPolicy reports an operation available iff the host actually
// supports it (capability- and diagnostic-derived). It adds no overrides.
type RealCapabilityPolicy struct{}

func (RealCapabilityPolicy) Apply(map[string]HostOperationAvailability, bool) {}

// HubReadOnlyPolicy forces the named operations unavailable, modeling a hub
// that has not routed those write operations to its peers. When the host is
// offline it stamps the offline reason so the offline map stays uniform.
type HubReadOnlyPolicy struct {
	Ops    []string
	Reason string
}

func (p HubReadOnlyPolicy) Apply(result map[string]HostOperationAvailability, reachable bool) {
	reason := p.Reason
	if !reachable {
		reason = "Host is offline."
	}
	for _, op := range p.Ops {
		result[op] = unavailable(reason)
	}
}

// HostKeyedPolicy resolves a per-host policy from the host key before the
// standard Apply pass. A hub uses it to suppress operations it cannot route
// to a specific host — configured HTTP and SSH peers take writes over the
// fleet proxies, but a host the hub has no route to is read-only.
// Enrichment consults ForHost when the policy implements it; the returned
// policy's Apply runs as usual.
type HostKeyedPolicy interface {
	AvailabilityPolicy
	ForHost(hostKey string) AvailabilityPolicy
}

// policyForHost resolves the effective policy for a remote host.
func policyForHost(policy AvailabilityPolicy, hostKey string) AvailabilityPolicy {
	keyed, ok := policy.(HostKeyedPolicy)
	if !ok {
		return policy
	}
	return keyed.ForHost(hostKey)
}

// HostScopedPolicy applies different overrides to the local host and to
// remote hosts. A hub serves some write operations through its own API while
// not routing them to peers (for example project register/delete), so a
// single uniform override would either disable a working local operation or
// advertise an unrouted remote one. Apply (the AvailabilityPolicy contract)
// delegates to Remote; the enrichment layer applies Self to the local host.
// A nil side means no overrides for that side.
type HostScopedPolicy struct {
	Self   AvailabilityPolicy
	Remote AvailabilityPolicy
}

func (p HostScopedPolicy) Apply(result map[string]HostOperationAvailability, reachable bool) {
	if p.Remote != nil {
		p.Remote.Apply(result, reachable)
	}
}

// selfPolicy returns the policy to apply to the local host: HostScopedPolicy
// callers get their Self side; any other policy applies uniformly.
func selfPolicy(policy AvailabilityPolicy) AvailabilityPolicy {
	scoped, ok := policy.(HostScopedPolicy)
	if !ok {
		return policy
	}
	if scoped.Self == nil {
		return RealCapabilityPolicy{}
	}
	return scoped.Self
}

// DefaultMutationOps lists the write operations a read-only hub suppresses.
func DefaultMutationOps() []string {
	return []string{
		OpWorktreeCreate, OpPullRequestImport, OpWorktreeDelete,
		OpSessionEnsure, OpSessionKill,
		OpRepositoryClone, OpProjectAdd, OpProjectRemove,
	}
}

// OperationAvailabilityFromState computes per-operation availability from
// connection state, diagnostics, and command capabilities, then lets the
// policy apply any overrides (e.g. a read-only hub suppressing writes).
func OperationAvailabilityFromState(
	diags []HostDiagnostic,
	cmds CommandCapabilities,
	reachable bool,
	policy AvailabilityPolicy,
) map[string]HostOperationAvailability {
	result := make(map[string]HostOperationAvailability, len(operationDefs))

	if !reachable {
		offline := "Host is offline."
		for op := range operationDefs {
			result[op] = unavailable(offline)
		}
		policy.Apply(result, false)
		return result
	}

	blocked := make(map[string]string)
	for _, d := range diags {
		for _, op := range d.BlocksOperations {
			if _, exists := blocked[op]; !exists {
				blocked[op] = d.RecoverySuggestion
			}
		}
	}

	for op, def := range operationDefs {
		if reason, ok := blocked[op]; ok {
			result[op] = unavailable(reason)
			continue
		}
		if !def.capCheck(cmds) {
			result[op] = unavailable(def.reason)
			continue
		}
		result[op] = HostOperationAvailability{Available: true}
	}

	policy.Apply(result, true)
	return result
}
