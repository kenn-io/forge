package config

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExternalContextPersistence(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "context")
	path := writeConfig(t, fmt.Sprintf(`
[[external_context]]
id = "metrics"
name = "Metrics"
command = [%q, "--json"]
timeout = "7s"
`, executable))
	cfg, err := Load(path)
	require.NoError(t, err)
	saved := filepath.Join(t.TempDir(), "saved.toml")
	require.NoError(t, cfg.Save(saved))
	loaded, err := Load(saved)
	require.NoError(t, err)
	assert.Equal(t, []ExternalContextSource{{ID: "metrics", Name: "Metrics", Command: []string{executable, "--json"}, Timeout: "7s"}}, loaded.ExternalContext)
}

func TestExternalContextValidation(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
		want string
	}{
		{"default timeout", `id="metrics"
name="Metrics"
command=["/opt/example/context"]`, ""},
		{"relative command", `id="metrics"
name="Metrics"
command=["context"]`, "absolute"},
		{"missing command", `id="metrics"
name="Metrics"`, "command"},
		{"missing name", `id="metrics"
command=["/opt/example/context"]`, "name"},
		{"invalid id", `id="../metrics"
name="Metrics"
command=["/opt/example/context"]`, "id"},
		{"nonpositive timeout", `id="metrics"
name="Metrics"
command=["/opt/example/context"]
timeout="0s"`, "timeout"},
		{"invalid timeout", `id="metrics"
name="Metrics"
command=["/opt/example/context"]
timeout="later"`, "timeout"},
		{"duplicate id", `id="metrics"
name="Metrics"
command=["/opt/example/context"]
[[external_context]]
id="metrics"
name="Other"
command=["/opt/example/other"]`, "duplicate"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := strings.ReplaceAll(tt.body, "/opt/example/", filepath.ToSlash(t.TempDir())+"/")
			cfg, err := Load(writeConfig(t, "[[external_context]]\n"+body))
			if tt.want != "" {
				require.ErrorContains(t, err, tt.want)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "10s", cfg.ExternalContext[0].Timeout)
		})
	}
}
