package externalcontext

import (
	"reflect"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/stretchr/testify/assert"
)

func TestResultSchemaAcceptsNoCardAndValidatesPresentCards(t *testing.T) {
	registry := huma.NewMapRegistry("#/components/schemas/", huma.DefaultSchemaNamer)
	schema := registry.Schema(reflect.TypeFor[ExternalContextResult](), true, "")
	for _, tt := range []struct {
		name  string
		value map[string]any
		valid bool
	}{
		{"no card", map[string]any{"card": nil}, true},
		{"card", map[string]any{"card": map[string]any{"status": "success", "summary": "Ready"}}, true},
		{"missing envelope", map[string]any{}, false},
		{"invalid card", map[string]any{"card": true}, false},
		{"missing summary", map[string]any{"card": map[string]any{"status": "success"}}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			result := &huma.ValidateResult{}
			huma.Validate(registry, schema, &huma.PathBuffer{}, huma.ModeReadFromServer, tt.value, result)
			if tt.valid {
				assert.Empty(t, result.Errors)
			} else {
				assert.NotEmpty(t, result.Errors)
			}
		})
	}
}
