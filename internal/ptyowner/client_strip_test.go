package ptyowner

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestClientUpdateStripEnvVarsWidensMonotonically pins reload behavior:
// token names added after construction must strip from later owner
// sessions, and earlier names must never drop.
func TestClientUpdateStripEnvVarsWidensMonotonically(t *testing.T) {
	client := &Client{StripEnvVars: []string{"BOOT_TOKEN"}}
	client.UpdateStripEnvVars([]string{"RELOAD_TOKEN", ""})
	client.UpdateStripEnvVars([]string{"RELOAD_TOKEN"})

	out := client.ownerHelperEnvironment([]string{
		"PATH=/usr/bin",
		"BOOT_TOKEN=a",
		"RELOAD_TOKEN=b",
		"KEEP=value",
	})

	assert.ElementsMatch(t, []string{
		"PATH=/usr/bin",
		"KEEP=value",
	}, out)
}

// TestPtyOwnerShouldStripSessionVarFold mirrors the localruntime check:
// Windows resolves env var names case-insensitively.
func TestPtyOwnerShouldStripSessionVarFold(t *testing.T) {
	assert.True(t, shouldStripSessionVarFold("github_token", nil, true))
	assert.True(t, shouldStripSessionVarFold("my_token", []string{"MY_TOKEN"}, true))
	assert.False(t, shouldStripSessionVarFold("github_token", nil, false))
}
