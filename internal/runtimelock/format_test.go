package runtimelock

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFormatCollisionBannerWithMetadata(t *testing.T) {
	require := require.New(t)

	cerr := &CollisionError{
		DataDir:  "/home/u/.kenn/forge",
		LockPath: "/home/u/.kenn/forge/kenn-forge.lock",
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
	FormatCollisionBanner(&buf, cerr, "" /* configPath */, "/home/u/.kenn/forge/config.toml" /* defaultConfigPath */)

	want := `error: another kenn-forge instance is already running
  data_dir:     /home/u/.kenn/forge
  lock file:    /home/u/.kenn/forge/kenn-forge.lock
  running pid:  12345
  listening on: 127.0.0.1:8091
  started at:   2026-05-19T10:30:00Z
  version:      1.2.3

  Run ` + "`kenn-forge daemon status`" + ` to inspect it.
`
	require.Equal(want, buf.String())
}

func TestFormatCollisionBannerWithNonDefaultConfig(t *testing.T) {
	require := require.New(t)

	cerr := &CollisionError{
		DataDir:  "/home/u/.kenn/forge",
		LockPath: "/home/u/.kenn/forge/kenn-forge.lock",
		Metadata: &Metadata{
			PID:        12345,
			ListenAddr: "127.0.0.1:8091",
			StartedAt:  "2026-05-19T10:30:00Z",
			Version:    "1.2.3",
		},
	}

	var buf bytes.Buffer
	FormatCollisionBanner(&buf, cerr, "/etc/kenn-forge/alt.toml", "/home/u/.kenn/forge/config.toml")

	require.Contains(buf.String(), "Run `kenn-forge daemon status --config /etc/kenn-forge/alt.toml` to inspect it.")
}

func TestFormatCollisionBannerMetadataUnavailable(t *testing.T) {
	require := require.New(t)

	cerr := &CollisionError{
		DataDir:             "/home/u/.kenn/forge",
		LockPath:            "/home/u/.kenn/forge/kenn-forge.lock",
		MetadataUnavailable: ReasonMetadataMissing,
	}

	var buf bytes.Buffer
	FormatCollisionBanner(&buf, cerr, "", "/home/u/.kenn/forge/config.toml")

	want := `error: another kenn-forge instance is already running
  data_dir:     /home/u/.kenn/forge
  lock file:    /home/u/.kenn/forge/kenn-forge.lock
  metadata:     unavailable (daemon may be early in startup, or metadata is missing/corrupt)

  Run ` + "`kenn-forge daemon status`" + ` to inspect it.
`
	require.Equal(want, buf.String())
}

func TestFormatStatusHumanRunningWithMetadata(t *testing.T) {
	require := require.New(t)

	st := Status{
		DataDir:  "/home/u/.kenn/forge",
		LockPath: "/home/u/.kenn/forge/kenn-forge.lock",
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
  data_dir:     /home/u/.kenn/forge
  lock file:    /home/u/.kenn/forge/kenn-forge.lock
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
		DataDir:             "/home/u/.kenn/forge",
		LockPath:            "/home/u/.kenn/forge/kenn-forge.lock",
		Running:             true,
		MetadataUnavailable: ReasonMetadataMissing,
	}

	var buf bytes.Buffer
	require.NoError(FormatStatus(&buf, st, false))

	want := `running (metadata unavailable: missing (daemon may be early in startup))
  data_dir:     /home/u/.kenn/forge
  lock file:    /home/u/.kenn/forge/kenn-forge.lock
`
	require.Equal(want, buf.String())
}

func TestFormatStatusHumanNotRunning(t *testing.T) {
	require := require.New(t)

	st := Status{
		DataDir:  "/home/u/.kenn/forge",
		LockPath: "/home/u/.kenn/forge/kenn-forge.lock",
	}

	var buf bytes.Buffer
	require.NoError(FormatStatus(&buf, st, false))

	want := `no running daemon
  data_dir:     /home/u/.kenn/forge
  lock file:    /home/u/.kenn/forge/kenn-forge.lock
`
	require.Equal(want, buf.String())
}

func TestFormatStatusJSONRunning(t *testing.T) {
	require := require.New(t)

	st := Status{
		DataDir:  "/dd",
		LockPath: "/dd/kenn-forge.lock",
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
  "lock_file": "/dd/kenn-forge.lock",
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
		LockPath: "/dd/kenn-forge.lock",
	}

	var buf bytes.Buffer
	require.NoError(FormatStatus(&buf, st, true))

	want := `{
  "running": false,
  "data_dir": "/dd",
  "lock_file": "/dd/kenn-forge.lock",
  "metadata": null
}
`
	require.Equal(want, buf.String())
}

func TestFormatStatusJSONMetadataUnavailable(t *testing.T) {
	require := require.New(t)

	st := Status{
		DataDir:             "/dd",
		LockPath:            "/dd/kenn-forge.lock",
		Running:             true,
		MetadataUnavailable: ReasonMetadataCorrupt,
	}

	var buf bytes.Buffer
	require.NoError(FormatStatus(&buf, st, true))

	want := `{
  "running": true,
  "data_dir": "/dd",
  "lock_file": "/dd/kenn-forge.lock",
  "metadata": null,
  "metadata_error": "corrupt"
}
`
	require.Equal(want, buf.String())
}
