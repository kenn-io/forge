package configreload

import (
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/kit/embedconfig"
)

// A settings write validates its clone, and validation rewrites each declared
// spoke while hub reconciliation reads the running config without that lock.
func TestCloneReloadedConfigDetachesDeclaredSpokes(t *testing.T) {
	running := &config.Config{Fleet: config.Fleet{
		DeclaredSpokes: []config.FleetDeclaredSpoke{{
			Name: "build-box", BaseURL: "https://spoke.example",
		}},
	}}

	candidate := CloneReloadedConfig(running)
	candidate.Fleet.DeclaredSpokes[0].Name = "mutated"

	require.Equal(t, "build-box", running.Fleet.DeclaredSpokes[0].Name)
}

func TestApplyConfigChangeWithoutConfigReturnsDisabledBeforeFileLoad(t *testing.T) {
	var cfg *config.Config
	var cfgMu sync.Mutex
	cfgPath := filepath.Join(t.TempDir(), "missing.toml")
	handlers := Handlers{
		Cfg:                       &cfg,
		CfgMu:                     &cfgMu,
		CfgPath:                   &cfgPath,
		UpdateRuntimeStripEnvVars: func(*config.Config) {},
	}

	event := handlers.ApplyConfigChange(t.Context())

	require.False(t, event.Valid)
	require.Equal(t, "config reload disabled: server has no in-memory config", event.Error)
}

func TestProviderHostRestartRequiredIsScopedToTheHost(t *testing.T) {
	boot := &config.Config{Platforms: []config.PlatformConfig{
		{Type: "gitea", Host: "gitea-a.example.test", BaseURL: "https://gitea-a.example.test", TokenEnv: "GITEA_A_TOKEN"},
		{Type: "gitea", Host: "gitea-b.example.test", BaseURL: "https://gitea-b.example.test", TokenEnv: "GITEA_B_TOKEN"},
	}}
	snapshot := SnapshotStartupConfig(boot)
	changed := func(edit func(*config.Config)) *config.Config {
		cfg := CloneReloadedConfig(boot)
		edit(&cfg)
		return &cfg
	}
	for _, tc := range []struct {
		name string
		cfg  *config.Config
		want bool
	}{{
		name: "own transport moved",
		cfg:  changed(func(c *config.Config) { c.Platforms[0].BaseURL = "https://moved.example.test" }),
		want: true,
	}, {
		name: "other host's transport moved",
		cfg:  changed(func(c *config.Config) { c.Platforms[1].BaseURL = "https://moved.example.test" }),
	}, {
		name: "own token renamed",
		cfg:  changed(func(c *config.Config) { c.Platforms[0].TokenEnv = "GITEA_A_ROTATED_TOKEN" }),
	}, {
		name: "host removed",
		cfg:  changed(func(c *config.Config) { c.Platforms = c.Platforms[1:] }),
		want: true,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, snapshot.ProviderHostRestartRequired(tc.cfg, "gitea", "gitea-a.example.test"))
		})
	}
}

func TestEmbeddingsChangeRequiresRestart(t *testing.T) {
	boot := &config.Config{}
	snapshot := SnapshotStartupConfig(boot)
	require.False(t, snapshot.RestartRequiredFor(boot))

	enabled := *boot
	enabled.Embeddings = embedconfig.Embedder{
		BaseURL: "http://127.0.0.1:11434/v1", Model: "nomic-embed-text", Dims: 768,
	}
	require.True(t, snapshot.RestartRequiredFor(&enabled), "the encoder is built once at startup")
}
