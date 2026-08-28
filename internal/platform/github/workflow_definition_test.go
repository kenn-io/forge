package github

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/platform"
)

func TestParseManualWorkflow(t *testing.T) {
	t.Run("preserves metadata and typed inputs in declaration order", func(t *testing.T) {
		content := []byte(`name: Release
on:
  workflow_dispatch:
    inputs:
      version:
        description: Release version
        required: true
        type: string
      dry_run:
        description: Do not publish
        required: true
        default: false
        type: boolean
      retries:
        default: 2
        type: number
      channel:
        default: stable
        type: choice
        options: [stable, beta]
      environment:
        required: true
        type: environment
`)

		definition, manual, err := ParseManualWorkflow(
			"Release",
			".github/workflows/release.yml",
			"https://github.com/acme/widget/actions/workflows/release.yml",
			"0123456789abcdef",
			content,
		)
		require.NoError(t, err)
		require.True(t, manual)
		assert.Equal(t, platform.WorkflowDefinition{
			ID:            ".github/workflows/release.yml",
			Name:          "Release",
			Path:          ".github/workflows/release.yml",
			WebURL:        "https://github.com/acme/widget/actions/workflows/release.yml",
			DefinitionSHA: "0123456789abcdef",
			Inputs: []platform.WorkflowInput{
				{
					Name:        "version",
					Description: "Release version",
					Required:    true,
					Type:        platform.WorkflowInputString,
				},
				{
					Name:        "dry_run",
					Description: "Do not publish",
					Required:    true,
					Type:        platform.WorkflowInputBoolean,
					Default:     false,
					HasDefault:  true,
				},
				{
					Name:       "retries",
					Type:       platform.WorkflowInputNumber,
					Default:    2,
					HasDefault: true,
				},
				{
					Name:       "channel",
					Type:       platform.WorkflowInputChoice,
					Default:    "stable",
					HasDefault: true,
					Options:    []string{"stable", "beta"},
				},
				{
					Name:     "environment",
					Required: true,
					Type:     platform.WorkflowInputEnvironment,
				},
			},
			Available: true,
		}, definition)
	})

	for _, test := range []struct {
		name    string
		content string
		manual  bool
		inputs  []platform.WorkflowInput
	}{
		{
			name:    "scalar trigger keeps on as a YAML string key",
			content: "on: workflow_dispatch\n",
			manual:  true,
		},
		{
			name:    "sequence trigger",
			content: "on: [push, workflow_dispatch]\n",
			manual:  true,
		},
		{
			name:    "mapping without workflow dispatch",
			content: "on:\n  push:\n    branches: [main]\n",
			manual:  false,
		},
		{
			name:    "manual mapping without inputs",
			content: "on:\n  workflow_dispatch:\n",
			manual:  true,
		},
		{
			name: "missing type defaults to string",
			content: "on:\n  workflow_dispatch:\n    inputs:\n      label:\n" +
				"        description: Build label\n        default: candidate\n",
			manual: true,
			inputs: []platform.WorkflowInput{{
				Name:        "label",
				Description: "Build label",
				Type:        platform.WorkflowInputString,
				Default:     "candidate",
				HasDefault:  true,
			}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			definition, manual, err := ParseManualWorkflow(
				"CI", ".github/workflows/ci.yml", "https://example.test/ci", "sha", []byte(test.content),
			)
			require.NoError(t, err)
			assert.Equal(t, test.manual, manual)
			assert.Equal(t, test.inputs, definition.Inputs)
			assert.Equal(t, ".github/workflows/ci.yml", definition.ID)
			assert.Equal(t, "CI", definition.Name)
			assert.True(t, definition.Available)
		})
	}

	for _, test := range []struct {
		name    string
		content []byte
	}{
		{
			name:    "unsupported input type",
			content: []byte("on:\n  workflow_dispatch:\n    inputs:\n      target:\n        type: object\n"),
		},
		{
			name:    "duplicate input key",
			content: []byte("on:\n  workflow_dispatch:\n    inputs:\n      target:\n        type: string\n      target:\n        type: string\n"),
		},
		{
			name:    "choice without options",
			content: []byte("on:\n  workflow_dispatch:\n    inputs:\n      channel:\n        type: choice\n"),
		},
		{
			name:    "choice with empty options",
			content: []byte("on:\n  workflow_dispatch:\n    inputs:\n      channel:\n        type: choice\n        options: []\n"),
		},
		{
			name:    "default outside choices",
			content: []byte("on:\n  workflow_dispatch:\n    inputs:\n      channel:\n        type: choice\n        options: [stable, beta]\n        default: nightly\n"),
		},
		{
			name:    "alias",
			content: []byte("dispatch: &dispatch workflow_dispatch\non: *dispatch\n"),
		},
		{
			name:    "mapping default",
			content: []byte("on:\n  workflow_dispatch:\n    inputs:\n      target:\n        type: string\n        default: {name: main}\n"),
		},
		{
			name:    "sequence default",
			content: []byte("on:\n  workflow_dispatch:\n    inputs:\n      target:\n        type: string\n        default: [main]\n"),
		},
		{
			name:    "scalar choice options",
			content: []byte("on:\n  workflow_dispatch:\n    inputs:\n      channel:\n        type: choice\n        options: stable\n"),
		},
		{
			name:    "non scalar choice option",
			content: []byte("on:\n  workflow_dispatch:\n    inputs:\n      channel:\n        type: choice\n        options: [stable, {name: beta}]\n"),
		},
		{
			name:    "boolean input with string default",
			content: []byte("on:\n  workflow_dispatch:\n    inputs:\n      dry_run:\n        type: boolean\n        default: no\n"),
		},
		{
			name:    "number input with string default",
			content: []byte("on:\n  workflow_dispatch:\n    inputs:\n      retries:\n        type: number\n        default: two\n"),
		},
		{
			name:    "string input with boolean default",
			content: []byte("on:\n  workflow_dispatch:\n    inputs:\n      target:\n        type: string\n        default: false\n"),
		},
		{
			name:    "non boolean required",
			content: []byte("on:\n  workflow_dispatch:\n    inputs:\n      target:\n        required: yes\n"),
		},
		{
			name:    "non string description",
			content: []byte("on:\n  workflow_dispatch:\n    inputs:\n      target:\n        description: 42\n"),
		},
		{
			name:    "non mapping inputs",
			content: []byte("on:\n  workflow_dispatch:\n    inputs: [target]\n"),
		},
		{
			name:    "non mapping input definition",
			content: []byte("on:\n  workflow_dispatch:\n    inputs:\n      target: string\n"),
		},
		{
			name:    "non scalar sequence trigger after workflow dispatch",
			content: []byte("on: [workflow_dispatch, {push: null}]\n"),
		},
		{
			name:    "multiple YAML documents",
			content: []byte("on: workflow_dispatch\n---\nname: trailing\n"),
		},
		{
			name:    "unknown workflow dispatch field",
			content: []byte("on:\n  workflow_dispatch:\n    inputz: {}\n"),
		},
		{
			name:    "misspelled required field",
			content: []byte("on:\n  workflow_dispatch:\n    inputs:\n      target:\n        requred: true\n"),
		},
		{
			name:    "misspelled default field",
			content: []byte("on:\n  workflow_dispatch:\n    inputs:\n      target:\n        defualt: main\n"),
		},
		{
			name:    "malformed YAML",
			content: []byte("on: [workflow_dispatch\n"),
		},
		{
			name:    "payload larger than limit",
			content: bytes.Repeat([]byte("x"), MaxWorkflowDefinitionBytes+1),
		},
	} {
		t.Run("rejects "+test.name, func(t *testing.T) {
			_, _, err := ParseManualWorkflow("CI", "ci.yml", "https://example.test/ci", "sha", test.content)
			require.Error(t, err)
		})
	}
}
