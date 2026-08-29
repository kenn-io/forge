package fleetsetup

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRenderSystemdServiceKeepsArgumentsOutOfShell(t *testing.T) {
	assert := assert.New(t)
	content := string(renderSystemdUserService(Plan{
		BinaryPath: "/opt/Kenn Forge/kenn-forge",
		ConfigPath: "/home/operator/Forge Config/config.toml",
		HomeDir:    "/home/operator", PathEnv: "/usr/local/bin:/usr/bin",
	}))

	assert.Contains(content, `ExecStart="/opt/Kenn Forge/kenn-forge" serve --config "/home/operator/Forge Config/config.toml"`)
	assert.NotContains(content, "/bin/sh")
	assert.NotContains(content, "tailscale")
	assert.Contains(content, managedFileMarker)
	assert.Contains(content, "KillMode=process")
	assert.Contains(content, "Environment=KENN_FORGE_DEV_RESTART=1")
}

func TestRenderLaunchAgentEscapesArguments(t *testing.T) {
	assert := assert.New(t)
	content := string(renderLaunchAgent(Plan{
		BinaryPath: "/Applications/Kenn & Forge/kenn-forge",
		ConfigPath: "/Users/operator/<forge>/config.toml",
		HomeDir:    "/Users/operator", DataDir: "/Users/operator/.kenn/forge",
		PathEnv: "/usr/local/bin:/usr/bin",
	}))

	assert.Contains(content, "Kenn &amp; Forge")
	assert.Contains(content, "&lt;forge&gt;")
	assert.NotContains(content, "/bin/sh")
	assert.Contains(content, managedFileMarker)
}
