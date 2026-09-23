package server

import (
	"fmt"
	"maps"
	"net/http"
	"sync"

	"go.kenn.io/forge/internal/server/routepolicy"
)

func providerRouteRules() (map[string]routepolicy.ProviderRouteRule, error) {
	registered, err := RegisteredTransportOperations()
	if err != nil {
		return nil, err
	}
	return routepolicy.BuildProviderRouteRules(registered, routepolicy.ProviderRouteDeclarations)
}

// ProviderRouteRules returns a detached ownership table. Registration and
// tests guarantee that construction succeeds for the checked-in route graph.
func ProviderRouteRules() map[string]routepolicy.ProviderRouteRule {
	rules, err := providerRouteRules()
	if err != nil {
		panic(err)
	}
	return maps.Clone(rules)
}

var providerRouteIndex = sync.OnceValues(func() ([]routepolicy.ProviderRouteMatch, error) {
	operations, err := RegisteredTransportOperations()
	if err != nil {
		return nil, err
	}
	rules, err := routepolicy.BuildProviderRouteRules(operations, routepolicy.ProviderRouteDeclarations)
	if err != nil {
		return nil, err
	}
	matches := make([]routepolicy.ProviderRouteMatch, 0, len(operations))
	for _, operation := range operations {
		matches = append(matches, routepolicy.ProviderRouteMatch{
			Operation: operation,
			Rule:      rules[operation.ID],
		})
	}
	return matches, nil
})

func providerRouteRuleForRequest(
	method string, canonicalPath string,
) (routepolicy.ProviderRouteRule, bool) {
	if method == http.MethodHead {
		method = http.MethodGet
	}
	matches, err := providerRouteIndex()
	if err != nil {
		panic(fmt.Sprintf("build provider route index: %v", err))
	}
	for _, match := range matches {
		if match.Operation.Method == method &&
			routepolicy.ProviderPathMatches(match.Operation.Path, canonicalPath) {
			return match.Rule, true
		}
	}
	return routepolicy.ProviderRouteRule{}, false
}
