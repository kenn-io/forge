// Package paramcodec converts generated API client parameter structs to the
// map the request encoder expects.
package paramcodec

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
)

// numberLiteral keeps a JSON number's exact text. The request encoder prints
// fmt.Stringer values as-is, so large integer IDs never pass through float64
// and print in exponent form.
type numberLiteral string

func (n numberLiteral) String() string { return string(n) }

// Map converts a generated path or query parameter struct, preserving
// numbers exactly.
func Map(v any) (map[string]any, error) {
	if v == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var fields map[string]jsontext.Value
	if err := json.Unmarshal(encoded, &fields); err != nil {
		return nil, err
	}
	if fields == nil {
		return nil, nil
	}
	params := make(map[string]any, len(fields))
	for key, value := range fields {
		param, err := paramValue(value)
		if err != nil {
			return nil, err
		}
		params[key] = param
	}
	return params, nil
}

func paramValue(value jsontext.Value) (any, error) {
	switch value.Kind() {
	case '0':
		return numberLiteral(value), nil
	case '[':
		var items []jsontext.Value
		if err := json.Unmarshal(value, &items); err != nil {
			return nil, err
		}
		out := make([]any, len(items))
		for i, item := range items {
			converted, err := paramValue(item)
			if err != nil {
				return nil, err
			}
			out[i] = converted
		}
		return out, nil
	case '{':
		var fields map[string]jsontext.Value
		if err := json.Unmarshal(value, &fields); err != nil {
			return nil, err
		}
		out := make(map[string]any, len(fields))
		for key, field := range fields {
			converted, err := paramValue(field)
			if err != nil {
				return nil, err
			}
			out[key] = converted
		}
		return out, nil
	default:
		var out any
		if err := json.Unmarshal(value, &out); err != nil {
			return nil, err
		}
		return out, nil
	}
}
