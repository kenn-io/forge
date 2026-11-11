package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestWindowsCommandHint(t *testing.T) {
	assert.Equal(t, `kenn-forge daemon restart --config "C:\Program Files\forge.toml"`, quoteCommand([]string{"kenn-forge", "daemon", "restart", "--config", `C:\Program Files\forge.toml`}))
}
