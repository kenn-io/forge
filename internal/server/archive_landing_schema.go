package server

import (
	"reflect"
	"strings"

	"github.com/danielgtaylor/huma/v2"
)

// Huma supports nullable scalars but deliberately does not infer nullable
// object references. Evidence distinguishes absent objects from zero values.
// Publish that distinction as JSON Schema anyOf, with the generated Go type
// pinned to its nullable pointer rather than an opaque union wrapper.
func completeLandedSchemas(registry huma.Registry) {
	for name, schema := range registry.Map() {
		typ := registry.TypeFromRef("#/components/schemas/" + name)
		if typ == nil || typ.Kind() != reflect.Struct {
			continue
		}
		switch typ.PkgPath() {
		case "go.kenn.io/forge/platform", "go.kenn.io/forge/landedwork", "go.kenn.io/forge/internal/archive/report":
		default:
			continue
		}
		for field := range typ.Fields() {
			if field.Type.Kind() != reflect.Pointer || field.Type.Elem().Kind() != reflect.Struct {
				continue
			}
			fieldName, options, _ := strings.Cut(field.Tag.Get("json"), ",")
			if strings.Contains(options, "omitempty") {
				continue
			}
			property := schema.Properties[fieldName]
			if property == nil || property.Ref == "" {
				continue
			}
			reference := property.Ref
			schema.Properties[fieldName] = &huma.Schema{
				AnyOf:      []*huma.Schema{{Ref: reference}, {Type: "null"}},
				Extensions: map[string]any{"x-go-type": "*" + strings.TrimPrefix(reference, "#/components/schemas/")},
			}
		}
	}
}
