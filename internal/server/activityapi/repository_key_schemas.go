package activityapi

import (
	"fmt"
	"reflect"

	"github.com/danielgtaylor/huma/v2"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/externalcontext"
	"go.kenn.io/forge/internal/fleet"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/itemapi"
	"go.kenn.io/forge/internal/server/spokeapi"
	"go.kenn.io/forge/internal/server/workspaceapi"
	"go.kenn.io/forge/platform"
)

// repositoryKeyAPITypes are the API types that carry a repokey-tagged
// platform.RepositoryKey. The key's fields are unexported, so huma documents
// each type by the flat wire struct its JSON methods encode, under the type's
// own schema name.
var repositoryKeyAPITypes = []reflect.Type{
	reflect.TypeFor[platform.RepositoryIdentity](),
	reflect.TypeFor[db.WorkspaceLaunchRepository](),
	reflect.TypeFor[db.ProviderStateRepository](),
	reflect.TypeFor[providerplane.RepositoryDescriptor](),
	reflect.TypeFor[providerplane.RepositoryDescriptorRequest](),
	reflect.TypeFor[providerplane.WorkspaceLaunchRequest](),
	reflect.TypeFor[fleet.RepositoryIdentity](),
	reflect.TypeFor[fleet.WorkspaceRepositorySummary](),
	reflect.TypeFor[httpapi.RepoRefResponse](),
	reflect.TypeFor[ghclient.ConfiguredRepoStatus](),
	reflect.TypeFor[workspaceapi.ProviderWorkspaceItemRequest](),
	reflect.TypeFor[externalcontext.PullRequest](),
	reflect.TypeFor[spokeapi.ProviderRepositoryObservation](),
	reflect.TypeFor[itemapi.ActivityRepoRefResponse](),
	reflect.TypeFor[spokeapi.FederationActivityRepositoryIdentity](),
	reflect.TypeFor[spokeapi.FederationWorkflowRepositoryIdentity](),
	reflect.TypeFor[spokeapi.FederationWorkflowItemIdentity](),
}

// WithRepositoryKeyWireSchemas replaces config's schema registry with one that
// documents each key-bearing API type by its flat wire form, keeping the
// type's schema name so the OpenAPI document and generated clients keep
// their existing names.
func WithRepositoryKeyWireSchemas(config huma.Config) huma.Config {
	names := make(map[reflect.Type]string, len(repositoryKeyAPITypes))
	wires := make(map[reflect.Type]reflect.Type, len(repositoryKeyAPITypes))
	for _, apiType := range repositoryKeyAPITypes {
		wire, ok, err := platform.RepositoryKeyWireType(apiType)
		if err != nil || !ok {
			panic(fmt.Sprintf("repository key API type %s: no wire form (%v)", apiType, err))
		}
		names[wire] = huma.DefaultSchemaNamer(apiType, "")
		wires[apiType] = wire
	}
	registry := huma.NewMapRegistry("#/components/schemas/", func(t reflect.Type, hint string) string {
		if name, ok := names[t]; ok {
			return name
		}
		return huma.DefaultSchemaNamer(t, hint)
	})
	for apiType, wire := range wires {
		registry.RegisterTypeAlias(apiType, wire)
	}
	config.Components.Schemas = registry
	return config
}
