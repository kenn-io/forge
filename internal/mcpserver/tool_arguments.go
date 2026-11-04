package mcpserver

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// openArgumentTypes accept fields they do not define. Agents and generated
// apps copy repository objects out of Forge results, such as a
// kenn_forge_list_repos row with its counts, into tool arguments. Every
// other argument object stays closed, so a misspelled filter still fails
// instead of silently widening a read.
var openArgumentTypes = []reflect.Type{reflect.TypeFor[repoFilterInput]()}

var openArgumentSchemas = sync.OnceValue(func() map[reflect.Type]*jsonschema.Schema {
	schemas := make(map[reflect.Type]*jsonschema.Schema, len(openArgumentTypes))
	for _, t := range openArgumentTypes {
		schema, err := jsonschema.ForType(t, &jsonschema.ForOptions{})
		if err != nil {
			panic(fmt.Sprintf("infer MCP argument schema for %v: %v", t, err))
		}
		schema.AdditionalProperties = nil
		schemas[t] = schema
	}
	return schemas
})

// addTool registers a typed tool like mcp.AddTool, but infers its input
// schema with openArgumentTypes left open, and records the fields those
// objects carried but the tool ignored under ignoredArgumentsMetaKey.
func addTool[In, Out any](server *mcp.Server, tool *mcp.Tool, handler mcp.ToolHandlerFor[In, Out]) {
	inType := reflect.TypeFor[In]()
	for inType.Kind() == reflect.Pointer {
		inType = inType.Elem()
	}
	schema, err := jsonschema.ForType(inType, &jsonschema.ForOptions{TypeSchemas: openArgumentSchemas()})
	if err != nil {
		panic(fmt.Sprintf("infer MCP input schema for %s: %v", tool.Name, err))
	}
	tool.InputSchema = schema
	mcp.AddTool(server, tool, func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		res, out, err := handler(ctx, req, in)
		if err != nil {
			return res, out, err
		}
		ignored := ignoredArguments(inType, req.Params.Arguments)
		if len(ignored) == 0 {
			return res, out, nil
		}
		if res == nil {
			res = &mcp.CallToolResult{}
		}
		if res.Meta == nil {
			res.Meta = mcp.Meta{}
		}
		res.Meta[ignoredArgumentsMetaKey] = ignored
		return res, out, nil
	})
}

// reportIgnoredArguments adds a text note naming ignored argument fields.
// It runs after the SDK fills in the result's text, which the SDK skips when
// a handler already supplied content. Some agent adapters drop _meta, so
// the note goes in the text the model reads.
func reportIgnoredArguments(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		result, err := next(ctx, method, req)
		res, ok := result.(*mcp.CallToolResult)
		if !ok || res == nil {
			return result, err
		}
		if ignored, ok := res.Meta[ignoredArgumentsMetaKey].([]string); ok && len(ignored) > 0 {
			res.Content = append(res.Content, &mcp.TextContent{Text: fmt.Sprintf(
				"Forge ignored argument fields this tool does not use: %s.", strings.Join(ignored, ", "),
			)})
		}
		return result, err
	}
}

// ignoredArguments returns the dotted paths of fields in raw that an open
// argument object of t carried but does not define.
func ignoredArguments(t reflect.Type, raw []byte) []string {
	var args any
	if len(raw) == 0 || json.Unmarshal(raw, &args) != nil {
		return nil
	}
	var ignored []string
	collectIgnoredArguments(t, args, "", &ignored)
	slices.Sort(ignored)
	return ignored
}

func collectIgnoredArguments(t reflect.Type, value any, path string, ignored *[]string) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Slice, reflect.Array:
		items, _ := value.([]any)
		for i, item := range items {
			collectIgnoredArguments(t.Elem(), item, path+"["+strconv.Itoa(i)+"]", ignored)
		}
	case reflect.Struct:
		object, _ := value.(map[string]any)
		if len(object) == 0 {
			return
		}
		fields := argumentFields(t)
		open := slices.Contains(openArgumentTypes, t)
		for key, field := range object {
			name := key
			if path != "" {
				name = path + "." + key
			}
			if fieldType, ok := fields[key]; ok {
				collectIgnoredArguments(fieldType, field, name, ignored)
			} else if open {
				*ignored = append(*ignored, name)
			}
		}
	default:
		// Scalars and maps hold no argument objects.
	}
}

// argumentFields maps the JSON names of t's fields to their types.
func argumentFields(t reflect.Type) map[string]reflect.Type {
	fields := make(map[string]reflect.Type)
	for _, field := range reflect.VisibleFields(t) {
		if !field.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "-" || (field.Anonymous && name == "") {
			continue
		}
		if name == "" {
			name = field.Name
		}
		fields[name] = field.Type
	}
	return fields
}
