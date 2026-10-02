package configreload

import (
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
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
