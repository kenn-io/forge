package server

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/federationauth"
	"go.kenn.io/forge/internal/server/routepolicy"
)

func TestProviderRouteCoverage(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	runParallelServerTest(t)

	registered, err := RegisteredTransportOperations()
	require.NoError(err)
	rules, err := providerRouteRules()
	require.NoError(err)

	seen := make(map[string]struct{}, len(registered))
	for _, operation := range registered {
		rule, ok := rules[operation.ID]
		require.Truef(ok, "operation %s has no ownership", operation.ID)
		_, duplicate := seen[operation.ID]
		require.Falsef(duplicate, "operation %s is registered twice", operation.ID)
		seen[operation.ID] = struct{}{}

		if rule.Owner != routepolicy.NodeLocal {
			assert.Contains([]federationauth.Scope{
				federationauth.ScopeProviderRead,
				federationauth.ScopeProviderWrite,
				federationauth.ScopeProviderHandoff,
			}, rule.PeerScope, operation.ID)
		}
		if operation.PeerCallable {
			require.NotEmpty(rule.PeerScope, operation.ID)
			assert.Equal(operation.PeerScope, rule.PeerScope, operation.ID)
		}
	}
	assert.Len(rules, len(registered))
}

func TestProviderRouteOwnershipExamples(t *testing.T) {
	assert := assert.New(t)
	runParallelServerTest(t)

	rules, err := providerRouteRules()
	require.NoError(t, err)
	assert.Equal(routepolicy.ProviderWithLocalOverlay, rules["list-pulls"].Owner)
	assert.Equal(routepolicy.NodeLocal, rules["get-settings"].Owner)
	assert.Equal(routepolicy.NodeLocal, rules["get-local-settings"].Owner)
	assert.Equal(routepolicy.ProviderHubOnly, rules["federation-get-provider-settings"].Owner)
	assert.Equal(routepolicy.ProviderHubOnly, rules["federation-query-workspace-provider-state"].Owner)
	assert.Equal(federationauth.ScopeProviderRead, rules["federation-query-workspace-provider-state"].PeerScope)
	assert.Equal(routepolicy.ProviderHubOnly, rules["merge-pull"].Owner)
	assert.Equal(routepolicy.NodeLocal, rules["get-pull-diff"].Owner)
	assert.Equal(routepolicy.NodeLocal, rules["get-workspace"].Owner)
	assert.Equal(routepolicy.ProviderHubOnly, rules["list-workflows"].Owner)
	assert.Equal(federationauth.ScopeProviderRead, rules["list-workflows"].PeerScope)
	assert.Equal(routepolicy.ProviderHubOnly, rules["dispatch-workflow"].Owner)
	assert.Equal(federationauth.ScopeProviderWrite, rules["dispatch-workflow"].PeerScope)
}
