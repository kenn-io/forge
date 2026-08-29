package fleet

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProjectForObserverSuppressesTwoHopMutation(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	full := Capabilities{Commands: CommandCapabilities{
		WorktreeCreate: true, WorktreeImportPR: true, WorktreeDelete: true,
		SessionEnsure: true, SessionKill: true, RepositoryClone: true,
		ProjectAdd: true, ProjectRemove: true,
	}}
	nodeA := NodeID("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	nodeB := NodeID("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	nodeC := NodeID("cccccccccccccccccccccccccccccccc")
	rawA := RawSnapshot{ProtocolVersion: 3, NodeID: nodeA,
		Host: RawHost{Hostname: "hub", Platform: "linux"}, Capabilities: &full}
	rawB := RawSnapshot{ProtocolVersion: 3, NodeID: nodeB,
		Host: RawHost{Hostname: "spoke-b", Platform: "linux"}, Capabilities: &full}
	rawC := RawSnapshot{ProtocolVersion: 3, NodeID: nodeC,
		Host: RawHost{Hostname: "spoke-c", Platform: "linux"}, Capabilities: &full}
	aggregate := BuildNeutralAggregate(rawA, []PeerResult{
		{NodeID: nodeB, Name: "spoke-b", Reachable: true, Raw: &rawB},
		{NodeID: nodeC, Name: "spoke-c", Reachable: true, Raw: &rawC},
	})

	projected := ProjectForObserver(aggregate, rawB, Observer{NodeID: nodeB, Role: RoleSpoke})
	var self, remoteC *HostSummary
	for index := range projected.Hosts {
		host := &projected.Hosts[index]
		switch host.ConfigKey {
		case string(nodeB):
			self = host
		case string(nodeC):
			remoteC = host
		}
	}
	require.NotNil(self)
	assert.Equal("self", self.Kind)
	require.NotNil(remoteC)
	write := remoteC.OperationAvailability[OpWorkspaceWrite]
	assert.False(write.Available)
	require.NotNil(write.UnavailableReason)
	assert.Equal(ReasonSummaryOnly, *write.UnavailableReason)

	hubView := ProjectForObserver(
		aggregate, rawA, Observer{NodeID: nodeA, Role: RoleHub},
	)
	for _, host := range hubView.Hosts {
		if host.ConfigKey == string(nodeC) {
			assert.True(host.OperationAvailability[OpWorkspaceWrite].Available)
		}
	}
}

func fullCommandCaps() CommandCapabilities {
	return CommandCapabilities{
		WorktreeCreate:   true,
		WorktreeImportPR: true,
		WorktreeDelete:   true,
		SessionEnsure:    true,
		SessionKill:      true,
	}
}

// TestHubReadOnlyPolicyCanForceMutationOpsUnavailable verifies the optional
// read-only policy still suppresses write operations regardless of host
// capabilities.
func TestHubReadOnlyPolicyCanForceMutationOpsUnavailable(t *testing.T) {
	caps := Capabilities{Dependencies: DependencyCapabilities{Git: true, Tmux: true, Gh: true},
		Commands: CommandCapabilities{WorktreeCreate: true, WorktreeDelete: true, SessionEnsure: true, SessionKill: true, WorktreeImportPR: true}}
	avail := OperationAvailabilityFromState(nil, caps.Commands, true, hubPolicy())
	for _, op := range []string{"worktreeCreate", "worktreeDelete", "sessionEnsure", "sessionKill", "pullRequestImport", "repositoryClone", "projectAdd", "projectRemove"} {
		assert.False(t, avail[op].Available, "%s must be unavailable under read-only policy", op)
	}
}

// TestOperationAvailability_ReadOnlyAvailable confirms non-mutating
// operations stay available when the host is reachable and capable while
// a read-only policy can still force mutation ops unavailable.
func TestOperationAvailability_ReadOnlyAvailable(t *testing.T) {
	assert := assert.New(t)
	avail := OperationAvailabilityFromState(
		nil, fullCommandCaps(), true, hubPolicy(),
	)

	// durableSessions is read-only (not a mutation op) and stays
	// available when its dependency capability is present.
	ds := avail[OpDurableSessions]
	assert.True(ds.Available, "durableSessions should be available")
	assert.Nil(ds.UnavailableReason)

	// All mutation ops are unavailable with the policy routing reason.
	for _, op := range DefaultMutationOps() {
		a := avail[op]
		assert.False(a.Available, "%s should be unavailable", op)
		assert.NotNil(a.UnavailableReason, "%s reason", op)
		assert.Contains(*a.UnavailableReason, "not routed", op)
	}
}

// TestOperationAvailability_PullRequestImportUnavailable confirms
// pull request import is unavailable when the capability is absent under
// the real capability policy.
func TestOperationAvailability_PullRequestImportUnavailable(t *testing.T) {
	cmds := fullCommandCaps()
	cmds.WorktreeImportPR = false

	avail := OperationAvailabilityFromState(nil, cmds, true, RealCapabilityPolicy{})

	pr := avail[OpPullRequestImport]
	assert.False(t, pr.Available)
	assert.NotNil(t, pr.UnavailableReason)
}

func TestOperationAvailability_OfflineHost(t *testing.T) {
	avail := OperationAvailabilityFromState(
		nil, fullCommandCaps(), false, hubPolicy(),
	)

	for op, a := range avail {
		assert.False(t, a.Available, "op %s offline", op)
		assert.NotNil(t, a.UnavailableReason)
		assert.Equal(t, "Host is offline.",
			*a.UnavailableReason, "op %s reason", op)
	}
}

// TestOperationAvailability_DiagnosticBlocksOperation confirms a
// diagnostic that blocks an operation yields an unavailable entry.
// Mutation ops (worktreeCreate, pullRequestImport) are unavailable
// either way; this exercises the diagnostic-indexing path.
func TestOperationAvailability_DiagnosticBlocksOperation(t *testing.T) {
	diags := []HostDiagnostic{
		{
			Code:               "missingGit",
			Severity:           "error",
			Summary:            "Missing git",
			RecoverySuggestion: "Install git on the host.",
			BlocksOperations: []string{
				OpWorktreeCreate,
				OpPullRequestImport,
			},
		},
	}

	avail := OperationAvailabilityFromState(
		diags, fullCommandCaps(), true, RealCapabilityPolicy{},
	)

	assert.False(t, avail[OpWorktreeCreate].Available)
	assert.False(t, avail[OpPullRequestImport].Available)
}

func hubPolicy() AvailabilityPolicy {
	return HubReadOnlyPolicy{Ops: DefaultMutationOps(), Reason: "Operation not routed by policy."}
}

// TestRealCapabilityPolicyAllowsCapableMutations is the crux of the refactor:
// under RealCapabilityPolicy a reachable, capable host reports its mutation
// operations AVAILABLE (a local daemon or routed hub performs them).
func TestRealCapabilityPolicyAllowsCapableMutations(t *testing.T) {
	assert := assert.New(t)
	avail := OperationAvailabilityFromState(nil, fullCommandCaps(), true, RealCapabilityPolicy{})
	for _, op := range []string{"worktreeCreate", "worktreeDelete", "sessionEnsure", "sessionKill", "pullRequestImport"} {
		a := avail[op]
		assert.True(a.Available, "%s must be available under RealCapabilityPolicy when capable", op)
		assert.Nil(a.UnavailableReason, "%s must have no reason when available", op)
	}
	// repositoryClone/projectAdd/projectRemove now have operationDefs, so a
	// real policy reports them; fullCommandCaps does not set those caps, so
	// they are present but unavailable (capability-derived), not absent.
	clone := avail[OpRepositoryClone]
	assert.False(clone.Available, "repositoryClone unavailable when capability absent")
	assert.NotNil(clone.UnavailableReason)
}

// TestRealCapabilityPolicyCloneAndProjectOps covers repositoryClone,
// projectAdd, and projectRemove under a real/local policy in both the
// supported (capability set -> available) and unsupported states.
func TestRealCapabilityPolicyCloneAndProjectOps(t *testing.T) {
	assert := assert.New(t)
	ops := []string{OpRepositoryClone, OpProjectAdd, OpProjectRemove}

	capable := CommandCapabilities{RepositoryClone: true, ProjectAdd: true, ProjectRemove: true}
	availCapable := OperationAvailabilityFromState(nil, capable, true, RealCapabilityPolicy{})
	for _, op := range ops {
		a := availCapable[op]
		assert.True(a.Available, "%s available when capable", op)
		assert.Nil(a.UnavailableReason, "%s has no reason when available", op)
	}

	availIncapable := OperationAvailabilityFromState(nil, CommandCapabilities{}, true, RealCapabilityPolicy{})
	for _, op := range ops {
		a := availIncapable[op]
		assert.False(a.Available, "%s unavailable when incapable", op)
		assert.NotNil(a.UnavailableReason, "%s has a reason when unavailable", op)
	}
}
