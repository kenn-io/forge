package github

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"slices"

	"go.kenn.io/forge/internal/platform"
	"go.yaml.in/yaml/v3"
)

const MaxWorkflowDefinitionBytes = 1 << 20

func ParseManualWorkflow(
	name string,
	path string,
	webURL string,
	definitionSHA string,
	content []byte,
) (platform.WorkflowDefinition, bool, error) {
	definition := platform.WorkflowDefinition{
		ID:            path,
		Name:          name,
		Path:          path,
		WebURL:        webURL,
		DefinitionSHA: definitionSHA,
		Available:     true,
	}
	if len(content) > MaxWorkflowDefinitionBytes {
		return definition, false, fmt.Errorf(
			"workflow definition exceeds %d-byte limit",
			MaxWorkflowDefinitionBytes,
		)
	}

	decoder := yaml.NewDecoder(bytes.NewReader(content))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return definition, false, fmt.Errorf("parse workflow definition: %w", err)
	}
	var trailing yaml.Node
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err != nil {
			return definition, false, fmt.Errorf("parse trailing workflow definition: %w", err)
		}
		return definition, false, fmt.Errorf("workflow definition must contain one document")
	}
	if len(document.Content) != 1 {
		return definition, false, fmt.Errorf("workflow definition must contain one document")
	}
	root := document.Content[0]
	if err := rejectAliases(root); err != nil {
		return definition, false, err
	}
	if root.Kind != yaml.MappingNode {
		return definition, false, fmt.Errorf("workflow definition must be a mapping")
	}

	onNode, found, err := mappingValue(root, "on")
	if err != nil {
		return definition, false, fmt.Errorf("parse workflow triggers: %w", err)
	}
	if !found {
		return definition, false, nil
	}

	manualNode, manual, err := manualTrigger(onNode)
	if err != nil {
		return definition, false, err
	}
	if !manual {
		return definition, false, nil
	}
	if manualNode == nil || isNull(manualNode) {
		return definition, true, nil
	}
	if manualNode.Kind != yaml.MappingNode {
		return definition, false, fmt.Errorf("workflow_dispatch configuration must be a mapping")
	}

	inputsNode, found, err := workflowDispatchInputs(manualNode)
	if err != nil {
		return definition, false, fmt.Errorf("parse workflow_dispatch: %w", err)
	}
	if !found || isNull(inputsNode) {
		return definition, true, nil
	}
	inputs, err := parseWorkflowInputs(inputsNode)
	if err != nil {
		return definition, false, err
	}
	definition.Inputs = inputs
	return definition, true, nil
}

func rejectAliases(root *yaml.Node) error {
	stack := []*yaml.Node{root}
	for len(stack) > 0 {
		last := len(stack) - 1
		node := stack[last]
		stack = stack[:last]
		if node.Kind == yaml.AliasNode {
			return fmt.Errorf("workflow definition must not contain YAML aliases")
		}
		stack = append(stack, node.Content...)
	}
	return nil
}

func mappingValue(mapping *yaml.Node, wanted string) (*yaml.Node, bool, error) {
	if mapping.Kind != yaml.MappingNode {
		return nil, false, fmt.Errorf("expected mapping")
	}
	var value *yaml.Node
	found := false
	seen := make(map[string]struct{}, len(mapping.Content)/2)
	for index := 0; index < len(mapping.Content); index += 2 {
		key := mapping.Content[index]
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
			return nil, false, fmt.Errorf("mapping keys must be strings")
		}
		if _, duplicate := seen[key.Value]; duplicate {
			return nil, false, fmt.Errorf("duplicate key %q", key.Value)
		}
		seen[key.Value] = struct{}{}
		if key.Value == wanted {
			value = mapping.Content[index+1]
			found = true
		}
	}
	return value, found, nil
}

func workflowDispatchInputs(mapping *yaml.Node) (*yaml.Node, bool, error) {
	var inputs *yaml.Node
	found := false
	for index := 0; index < len(mapping.Content); index += 2 {
		key := mapping.Content[index]
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
			return nil, false, fmt.Errorf("workflow_dispatch keys must be strings")
		}
		if key.Value != "inputs" {
			return nil, false, fmt.Errorf("unknown workflow_dispatch field %q", key.Value)
		}
		if found {
			return nil, false, fmt.Errorf("duplicate workflow_dispatch field %q", key.Value)
		}
		inputs = mapping.Content[index+1]
		found = true
	}
	return inputs, found, nil
}

func manualTrigger(onNode *yaml.Node) (*yaml.Node, bool, error) {
	switch onNode.Kind {
	case yaml.ScalarNode:
		if isNull(onNode) {
			return nil, false, nil
		}
		if onNode.Tag != "!!str" {
			return nil, false, fmt.Errorf("workflow trigger must be a string")
		}
		return nil, onNode.Value == "workflow_dispatch", nil
	case yaml.SequenceNode:
		manual := false
		for _, event := range onNode.Content {
			if event.Kind != yaml.ScalarNode || event.Tag != "!!str" {
				return nil, false, fmt.Errorf("workflow trigger sequence must contain strings")
			}
			if event.Value == "workflow_dispatch" {
				manual = true
			}
		}
		return nil, manual, nil
	case yaml.MappingNode:
		manualNode, found, err := mappingValue(onNode, "workflow_dispatch")
		if err != nil {
			return nil, false, fmt.Errorf("parse workflow trigger mapping: %w", err)
		}
		return manualNode, found, nil
	default:
		return nil, false, fmt.Errorf("workflow trigger must be a scalar, sequence, or mapping")
	}
}

func parseWorkflowInputs(inputsNode *yaml.Node) ([]platform.WorkflowInput, error) {
	if inputsNode.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("workflow_dispatch inputs must be a mapping")
	}

	inputs := make([]platform.WorkflowInput, 0, len(inputsNode.Content)/2)
	seen := make(map[string]struct{}, len(inputsNode.Content)/2)
	for index := 0; index < len(inputsNode.Content); index += 2 {
		nameNode := inputsNode.Content[index]
		definitionNode := inputsNode.Content[index+1]
		if nameNode.Kind != yaml.ScalarNode || nameNode.Tag != "!!str" {
			return nil, fmt.Errorf("workflow input names must be strings")
		}
		if _, duplicate := seen[nameNode.Value]; duplicate {
			return nil, fmt.Errorf("duplicate workflow input %q", nameNode.Value)
		}
		seen[nameNode.Value] = struct{}{}

		input, err := parseWorkflowInput(nameNode.Value, definitionNode)
		if err != nil {
			return nil, fmt.Errorf("parse workflow input %q: %w", nameNode.Value, err)
		}
		inputs = append(inputs, input)
	}
	return inputs, nil
}

func parseWorkflowInput(name string, definitionNode *yaml.Node) (platform.WorkflowInput, error) {
	input := platform.WorkflowInput{Name: name, Type: platform.WorkflowInputString}
	if isNull(definitionNode) {
		return input, nil
	}
	if definitionNode.Kind != yaml.MappingNode {
		return input, fmt.Errorf("definition must be a mapping")
	}

	fields, err := inputFields(definitionNode)
	if err != nil {
		return input, err
	}
	if node, ok := fields["description"]; ok {
		value, err := stringScalar(node, "description")
		if err != nil {
			return input, err
		}
		input.Description = value
	}
	if node, ok := fields["required"]; ok {
		if node.Kind != yaml.ScalarNode || node.Tag != "!!bool" {
			return input, fmt.Errorf("required must be a boolean")
		}
		if err := node.Decode(&input.Required); err != nil {
			return input, fmt.Errorf("decode required: %w", err)
		}
	}
	if node, ok := fields["type"]; ok {
		value, err := stringScalar(node, "type")
		if err != nil {
			return input, err
		}
		input.Type = platform.WorkflowInputType(value)
	}
	if !supportedWorkflowInputType(input.Type) {
		return input, fmt.Errorf("unsupported type %q", input.Type)
	}

	optionsNode, hasOptions := fields["options"]
	if input.Type == platform.WorkflowInputChoice {
		if !hasOptions {
			return input, fmt.Errorf("choice input requires options")
		}
		options, err := choiceOptions(optionsNode)
		if err != nil {
			return input, err
		}
		input.Options = options
	} else if hasOptions {
		return input, fmt.Errorf("options are only valid for choice inputs")
	}

	if defaultNode, ok := fields["default"]; ok {
		value, err := scalarDefault(defaultNode)
		if err != nil {
			return input, err
		}
		if !defaultMatchesType(value, input.Type) {
			return input, fmt.Errorf("default does not match type %q", input.Type)
		}
		if input.Type == platform.WorkflowInputChoice && !contains(input.Options, value.(string)) {
			return input, fmt.Errorf("default %q is not one of the choice options", value)
		}
		input.Default = value
		input.HasDefault = true
	}
	return input, nil
}

func inputFields(mapping *yaml.Node) (map[string]*yaml.Node, error) {
	fields := make(map[string]*yaml.Node, len(mapping.Content)/2)
	for index := 0; index < len(mapping.Content); index += 2 {
		key := mapping.Content[index]
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
			return nil, fmt.Errorf("definition keys must be strings")
		}
		switch key.Value {
		case "description", "required", "type", "default", "options":
		default:
			return nil, fmt.Errorf("unknown input definition field %q", key.Value)
		}
		if _, duplicate := fields[key.Value]; duplicate {
			return nil, fmt.Errorf("duplicate definition key %q", key.Value)
		}
		fields[key.Value] = mapping.Content[index+1]
	}
	return fields, nil
}

func supportedWorkflowInputType(inputType platform.WorkflowInputType) bool {
	switch inputType {
	case platform.WorkflowInputString,
		platform.WorkflowInputNumber,
		platform.WorkflowInputBoolean,
		platform.WorkflowInputChoice,
		platform.WorkflowInputEnvironment:
		return true
	default:
		return false
	}
}

func choiceOptions(optionsNode *yaml.Node) ([]string, error) {
	if optionsNode.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("choice options must be a sequence")
	}
	if len(optionsNode.Content) == 0 {
		return nil, fmt.Errorf("choice input requires at least one option")
	}

	options := make([]string, 0, len(optionsNode.Content))
	seen := make(map[string]struct{}, len(optionsNode.Content))
	for _, optionNode := range optionsNode.Content {
		option, err := stringScalar(optionNode, "choice option")
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[option]; duplicate {
			return nil, fmt.Errorf("duplicate choice option %q", option)
		}
		seen[option] = struct{}{}
		options = append(options, option)
	}
	return options, nil
}

func scalarDefault(node *yaml.Node) (any, error) {
	if node.Kind != yaml.ScalarNode {
		return nil, fmt.Errorf("default must be a scalar")
	}

	switch node.Tag {
	case "!!str":
		return node.Value, nil
	case "!!bool":
		var value bool
		if err := node.Decode(&value); err != nil {
			return nil, fmt.Errorf("decode boolean default: %w", err)
		}
		return value, nil
	case "!!int":
		var value any
		if err := node.Decode(&value); err != nil {
			return nil, fmt.Errorf("decode integer default: %w", err)
		}
		return value, nil
	case "!!float":
		var value float64
		if err := node.Decode(&value); err != nil {
			return nil, fmt.Errorf("decode number default: %w", err)
		}
		if math.IsInf(value, 0) || math.IsNaN(value) {
			return nil, fmt.Errorf("number default must be finite")
		}
		return value, nil
	default:
		return nil, fmt.Errorf("default has unsupported scalar type %q", node.Tag)
	}
}

func defaultMatchesType(value any, inputType platform.WorkflowInputType) bool {
	switch inputType {
	case platform.WorkflowInputString, platform.WorkflowInputChoice, platform.WorkflowInputEnvironment:
		_, ok := value.(string)
		return ok
	case platform.WorkflowInputBoolean:
		_, ok := value.(bool)
		return ok
	case platform.WorkflowInputNumber:
		switch value.(type) {
		case int, int8, int16, int32, int64,
			uint, uint8, uint16, uint32, uint64,
			float32, float64:
			return true
		default:
			return false
		}
	default:
		return false
	}
}

func stringScalar(node *yaml.Node, field string) (string, error) {
	if node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
		return "", fmt.Errorf("%s must be a string", field)
	}
	return node.Value, nil
}

func isNull(node *yaml.Node) bool {
	return node.Kind == yaml.ScalarNode && node.Tag == "!!null"
}

func contains(values []string, wanted string) bool {
	return slices.Contains(values, wanted)
}
