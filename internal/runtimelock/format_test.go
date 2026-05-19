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

func TestFormatStatusHumanRunningWithMetadata(t *testing.T) {
	require := require.New(t)

	st := Status{
		DataDir:  "/home/u/.config/middleman",
		LockPath: "/home/u/.config/middleman/middleman.lock",
		Running:  true,
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
	require.NoError(FormatStatus(&buf, st, false))

	want := `running
  data_dir:     /home/u/.config/middleman
  lock file:    /home/u/.config/middleman/middleman.lock
  pid:          12345
  host:         127.0.0.1
  port:         8091
  listen_addr:  127.0.0.1:8091
  started_at:   2026-05-19T10:30:00Z
  version:      1.2.3
  commit:       abcd1234
`
	require.Equal(want, buf.String())
}

func TestFormatStatusHumanRunningMetadataUnavailable(t *testing.T) {
	require := require.New(t)

	st := Status{
		DataDir:             "/home/u/.config/middleman",
		LockPath:            "/home/u/.config/middleman/middleman.lock",
		Running:             true,
		MetadataUnavailable: ReasonMetadataMissing,
	}

	var buf bytes.Buffer
	require.NoError(FormatStatus(&buf, st, false))

	want := `running (metadata unavailable: missing (daemon may be early in startup))
  data_dir:     /home/u/.config/middleman
  lock file:    /home/u/.config/middleman/middleman.lock
`
	require.Equal(want, buf.String())
}

func TestFormatStatusHumanNotRunning(t *testing.T) {
	require := require.New(t)

	st := Status{
		DataDir:  "/home/u/.config/middleman",
		LockPath: "/home/u/.config/middleman/middleman.lock",
	}

	var buf bytes.Buffer
	require.NoError(FormatStatus(&buf, st, false))

	want := `no running daemon
  data_dir:     /home/u/.config/middleman
  lock file:    /home/u/.config/middleman/middleman.lock
`
	require.Equal(want, buf.String())
}

func TestFormatStatusJSONRunning(t *testing.T) {
	require := require.New(t)

	st := Status{
		DataDir:  "/dd",
		LockPath: "/dd/middleman.lock",
		Running:  true,
		Metadata: &Metadata{
			PID:        4242,
			Host:       "127.0.0.1",
			Port:       8091,
			ListenAddr: "127.0.0.1:8091",
			StartedAt:  "2026-05-19T10:30:00Z",
			Version:    "v1",
			Commit:     "c1",
		},
	}

	var buf bytes.Buffer
	require.NoError(FormatStatus(&buf, st, true))

	want := `{
  "running": true,
  "data_dir": "/dd",
  "lock_file": "/dd/middleman.lock",
  "metadata": {
    "pid": 4242,
    "host": "127.0.0.1",
    "port": 8091,
    "listen_addr": "127.0.0.1:8091",
    "started_at": "2026-05-19T10:30:00Z",
    "version": "v1",
    "commit": "c1"
  }
}
`
	require.Equal(want, buf.String())
}

func TestFormatStatusJSONNotRunning(t *testing.T) {
	require := require.New(t)

	st := Status{
		DataDir:  "/dd",
		LockPath: "/dd/middleman.lock",
	}

	var buf bytes.Buffer
	require.NoError(FormatStatus(&buf, st, true))

	want := `{
  "running": false,
  "data_dir": "/dd",
  "lock_file": "/dd/middleman.lock",
  "metadata": null
}
`
	require.Equal(want, buf.String())
}

func TestFormatStatusJSONMetadataUnavailable(t *testing.T) {
	require := require.New(t)

	st := Status{
		DataDir:             "/dd",
		LockPath:            "/dd/middleman.lock",
		Running:             true,
		MetadataUnavailable: ReasonMetadataCorrupt,
	}

	var buf bytes.Buffer
	require.NoError(FormatStatus(&buf, st, true))

	want := `{
  "running": true,
  "data_dir": "/dd",
  "lock_file": "/dd/middleman.lock",
  "metadata": null,
  "metadata_error": "corrupt"
}
`
	require.Equal(want, buf.String())
}
