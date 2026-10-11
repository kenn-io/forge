package generated_test

import (
	"encoding/json/v2"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/apiclient/generated"
)

func TestGeneratedModelsPreserveArbitraryJSONValues(t *testing.T) {
	t.Run("workflow input default", func(t *testing.T) {
		var input generated.WorkflowInputResponse
		require.NoError(t, json.Unmarshal([]byte(`{"default":false}`), &input))
		require.Equal(t, false, input.Default)
	})
	t.Run("problem error value", func(t *testing.T) {
		var problem generated.ProblemError
		require.NoError(t, json.Unmarshal([]byte(`{"code":"badRequest","errors":[{"value":"rejected"}]}`), &problem))
		require.Len(t, problem.Errors, 1)
		require.Equal(t, "rejected", problem.Errors[0].Value)
	})
	t.Run("elicitation form fields", func(t *testing.T) {
		var request generated.ACPElicitation
		require.NoError(t, json.Unmarshal([]byte(`{"id":"e1","message":"Release?","schema":{"properties":{`+
			`"version":{"type":"string","title":"Version"},"publish":{"type":"boolean","default":true}},`+
			`"required":["version"]}}`), &request))
		require.Equal(t, map[string]any{"type": "string", "title": "Version"}, request.Schema.Properties["version"])
		require.Equal(t, map[string]any{"type": "boolean", "default": true}, request.Schema.Properties["publish"])
	})
}
