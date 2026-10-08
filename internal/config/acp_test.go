package config

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestACPConfigRoundTrip(t *testing.T) {
	path := writeConfig(t, `
[acp]
font_family = '  "MesloLGS NF", monospace  '
font_size = 18

[terminal]
font_family = '"Iosevka Term", monospace'
font_size = 12
`)
	cfg, err := Load(path)
	require.NoError(t, err)
	savedPath := filepath.Join(t.TempDir(), "saved.toml")
	require.NoError(t, cfg.Save(savedPath))
	reloaded, err := Load(savedPath)
	require.NoError(t, err)
	assert := assert.New(t)
	assert.Equal(`"MesloLGS NF", monospace`, reloaded.ACP.FontFamily)
	assert.Equal(18, reloaded.ACP.FontSize)
	assert.Equal(`"Iosevka Term", monospace`, reloaded.Terminal.FontFamily)
	assert.Equal(12, reloaded.Terminal.FontSize)
}

func TestACPFontSizeValidation(t *testing.T) {
	for _, tt := range []struct {
		size int
		want int
	}{
		{0, 13}, {8, 8}, {32, 32}, {-1, 0}, {7, 0}, {33, 0},
	} {
		t.Run(fmt.Sprint(tt.size), func(t *testing.T) {
			cfg, err := Load(writeConfig(t, fmt.Sprintf("[acp]\nfont_size = %d\n", tt.size)))
			if tt.want == 0 {
				require.ErrorContains(t, err, "acp.font_size")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, cfg.ACP.FontSize)
		})
	}
}
