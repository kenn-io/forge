package configreload

import (
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/kit/embedconfig"
)

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
