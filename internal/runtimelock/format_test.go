package runtimelock

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFormatCollisionBannerWithMetadata(t *testing.T) {
	require := require.New(t)

	cerr := &CollisionError{
		DataDir:  "/home/u/.config/middleman",
		LockPath: "/home/u/.config/middleman/middleman.lock",
		Metadata: &Metadata{
			PID:        12345,
			Host:       "127.0.0.1",
			Port:       8091,
			ListenAddr: "127.0.0.1:8091",
			StartedAt:  "2026-05-19T10:30:00Z",
			Version:    "1.2.3",
			Commit:     "abcd1234",
		},
	}

	var buf bytes.Buffer
	FormatCollisionBanner(&buf, cerr, "" /* configPath */, "/home/u/.config/middleman/config.toml" /* defaultConfigPath */)

	want := `error: another middleman instance is already running
  data_dir:     /home/u/.config/middleman
  lock file:    /home/u/.config/middleman/middleman.lock
  running pid:  12345
  listening on: 127.0.0.1:8091
  started at:   2026-05-19T10:30:00Z
  version:      1.2.3

  Run ` + "`middleman status`" + ` to inspect it.
`
	require.Equal(want, buf.String())
}

func TestFormatCollisionBannerWithNonDefaultConfig(t *testing.T) {
	require := require.New(t)

	cerr := &CollisionError{
		DataDir:  "/home/u/.config/middleman",
		LockPath: "/home/u/.config/middleman/middleman.lock",
		Metadata: &Metadata{
			PID:        12345,
			ListenAddr: "127.0.0.1:8091",
			StartedAt:  "2026-05-19T10:30:00Z",
			Version:    "1.2.3",
		},
	}

	var buf bytes.Buffer
	FormatCollisionBanner(&buf, cerr, "/etc/middleman/alt.toml", "/home/u/.config/middleman/config.toml")

	require.Contains(buf.String(), "Run `middleman status --config /etc/middleman/alt.toml` to inspect it.")
}

func TestFormatCollisionBannerMetadataUnavailable(t *testing.T) {
	require := require.New(t)

	cerr := &CollisionError{
		DataDir:             "/home/u/.config/middleman",
		LockPath:            "/home/u/.config/middleman/middleman.lock",
		MetadataUnavailable: ReasonMetadataMissing,
	}

	var buf bytes.Buffer
	FormatCollisionBanner(&buf, cerr, "", "/home/u/.config/middleman/config.toml")

	want := `error: another middleman instance is already running
  data_dir:     /home/u/.config/middleman
  lock file:    /home/u/.config/middleman/middleman.lock
  metadata:     unavailable (daemon may be early in startup, or metadata is missing/corrupt)

  Run ` + "`middleman status`" + ` to inspect it.
`
	require.Equal(want, buf.String())
}
