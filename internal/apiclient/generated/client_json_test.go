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
}
