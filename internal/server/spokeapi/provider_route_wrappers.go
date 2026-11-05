package spokeapi

import (
	"go.kenn.io/forge/internal/mcpserver"
	"go.kenn.io/forge/platform"
)

type FederationWorkflowItemIdentity mcpserver.ItemIdentity

// FederationWorkflowItemIdentity flattens the repository key in the same way as MCP identities.

func (i FederationWorkflowItemIdentity) MarshalJSON() ([]byte, error) {
	type plain FederationWorkflowItemIdentity
	return platform.MarshalKeyedJSON(plain(i))
}

func (i *FederationWorkflowItemIdentity) UnmarshalJSON(data []byte) error {
	type plain FederationWorkflowItemIdentity
	return platform.UnmarshalKeyedJSON(data, (*plain)(i))
}
