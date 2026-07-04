package server

import (
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkflowStateOpenAPIConstrainsWriteBody(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	doc := NewOpenAPI()
	schema, ok := doc.Components.Schemas.Map()["SetWorkflowStateBody"]
	require.True(ok)

	status := schema.Properties["status"]
	require.NotNil(status)
	assert.Equal([]any{"new", "reviewing", "waiting", "awaiting_merge"}, status.Enum)

	expected := schema.Properties["expected_status"]
	require.NotNil(expected)
	assert.Equal([]any{"new", "reviewing", "waiting", "awaiting_merge"}, expected.Enum)
	assert.Contains(expected.Description, "Required unless force is true")
	assert.Contains(expected.Description, "mutually exclusive")

	force := schema.Properties["force"]
	require.NotNil(force)
	assert.Equal("boolean", string(force.Type))
	assert.Contains(force.Description, "deliberate unconditional override")
	assert.Contains(force.Description, "mutually exclusive")
	contract, ok := schema.Extensions["oneOf"].([]*huma.Schema)
	require.True(ok)
	require.Len(contract, 2)
	assert.Contains(contract[0].Required, "expected_status")
	assert.Equal([]any{false}, contract[0].Properties["force"].Enum)
	assert.Contains(contract[1].Required, "force")
	assert.Equal([]any{true}, contract[1].Properties["force"].Enum)
	require.NotNil(contract[1].Not)
	assert.Contains(contract[1].Not.Required, "expected_status")

	source := schema.Properties["source"]
	require.NotNil(source)
	assert.Equal("^[a-z][a-z0-9_-]{0,39}$", source.Pattern)

	actor := schema.Properties["actor"]
	reason := schema.Properties["reason"]
	require.NotNil(actor)
	require.NotNil(reason)
	assert.Equal(120, *actor.MaxLength)
	assert.Equal(500, *reason.MaxLength)
}
