package server

import (
	"encoding/json"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/santhosh-tekuri/jsonschema/v6"
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
	assert.Contains(expected.Description, "Omit force")

	force := schema.Properties["force"]
	require.NotNil(force)
	assert.Equal("boolean", string(force.Type))
	assert.Contains(force.Description, "deliberate unconditional override")
	assert.Contains(force.Description, "false is invalid")
	contract, ok := schema.Extensions["oneOf"].([]*huma.Schema)
	require.True(ok)
	require.Len(contract, 2)
	assert.Nil(schema.AdditionalProperties)
	assert.NotContains(schema.Extensions, "additionalProperties")
	assert.Contains(contract[0].Required, "status")
	assert.Contains(contract[0].Required, "expected_status")
	assert.NotContains(contract[0].Properties, "force")
	assert.Contains(contract[1].Required, "status")
	assert.Contains(contract[1].Required, "force")
	assert.Equal([]any{true}, contract[1].Properties["force"].Enum)
	assert.NotContains(contract[1].Properties, "expected_status")
	openAPIProperties, ok := schema.Extensions["properties"].(map[string]*huma.Schema)
	require.True(ok)
	assert.Empty(openAPIProperties)
	assert.NotContains(openAPIProperties, "expected_status")
	assert.NotContains(openAPIProperties, "force")
	assertValidWorkflowStateOpenAPISchema(t, schema)

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

func assertValidWorkflowStateOpenAPISchema(t *testing.T, schema *huma.Schema) {
	t.Helper()
	require := require.New(t)
	raw, err := json.Marshal(schema)
	require.NoError(err)
	var doc any
	require.NoError(json.Unmarshal(raw, &doc))
	compiler := jsonschema.NewCompiler()
	require.NoError(compiler.AddResource("schema.json", doc))
	compiled, err := compiler.Compile("schema.json")
	require.NoError(err)

	for _, body := range []map[string]any{
		{"status": "reviewing", "expected_status": "new"},
		{"status": "waiting", "force": true, "actor": "mcp"},
	} {
		require.NoError(compiled.Validate(body))
	}
	for _, body := range []map[string]any{
		{"status": "reviewing"},
		{"status": "reviewing", "force": false},
		{"status": "reviewing", "expected_status": "new", "force": false},
		{"status": "reviewing", "force": true, "expected_status": "new"},
		{"status": "reviewing", "force": true, "unexpected": "field"},
	} {
		require.Error(compiled.Validate(body))
	}
}
