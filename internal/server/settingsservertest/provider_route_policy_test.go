package settingsservertest

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/server/routepolicy"
	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
)

func TestProviderRouteCoverageRejectsUnknownAndDuplicateOperations(t *testing.T) {
	serverfake.RunParallelServerTest(t)

	registered := []routepolicy.RegisteredTransportOperation{{ID: "known"}}
	_, err := routepolicy.BuildProviderRouteRules(registered, []routepolicy.ProviderRouteRule{{
		OperationID: "known", Owner: routepolicy.NodeLocal,
	}, {
		OperationID: "known", Owner: routepolicy.NodeLocal,
	}})
	require.ErrorContains(t, err, "duplicate")

	_, err = routepolicy.BuildProviderRouteRules(registered, nil)
	require.ErrorContains(t, err, "has no ownership")

	_, err = routepolicy.BuildProviderRouteRules(registered, []routepolicy.ProviderRouteRule{{
		OperationID: "unknown", Owner: routepolicy.NodeLocal,
	}})
	require.ErrorContains(t, err, "unknown operation")
}
