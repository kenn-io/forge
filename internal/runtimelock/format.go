package runtimelock

import (
	"encoding/json"
	"fmt"
	"io"
)

// FormatCollisionBanner writes a multi-line human-readable banner to w
// describing the collision. configPath and defaultConfigPath are used
// to render the "Run `middleman status [--config ...]`" hint; when
// configPath is empty or equals defaultConfigPath, the flag is
// omitted.
//
// When cerr.Metadata is nil, the per-field lines collapse to a single
// "metadata: unavailable (...)" line.
func FormatCollisionBanner(w io.Writer, cerr *CollisionError, configPath, defaultConfigPath string) {
	fmt.Fprintln(w, "error: another middleman instance is already running")
	fmt.Fprintf(w, "  data_dir:     %s\n", cerr.DataDir)
	fmt.Fprintf(w, "  lock file:    %s\n", cerr.LockPath)

	if cerr.Metadata != nil {
		m := cerr.Metadata
		fmt.Fprintf(w, "  running pid:  %d\n", m.PID)
		fmt.Fprintf(w, "  listening on: %s\n", m.ListenAddr)
		fmt.Fprintf(w, "  started at:   %s\n", m.StartedAt)
		if m.Version != "" {
			fmt.Fprintf(w, "  version:      %s\n", m.Version)
		}
	} else {
		fmt.Fprintln(w, "  metadata:     unavailable (daemon may be early in startup, or metadata is missing/corrupt)")
	}

	fmt.Fprintln(w)
	if configPath != "" && configPath != defaultConfigPath {
		fmt.Fprintf(w, "  Run `middleman status --config %s` to inspect it.\n", configPath)
	} else {
		fmt.Fprintln(w, "  Run `middleman status` to inspect it.")
	}
}

// statusJSON is the wire shape of FormatStatus(..., asJSON=true).
// Defined explicitly so the JSON keys do not depend on a field-order
// accident in Status.
type statusJSON struct {
	Running       bool      `json:"running"`
	DataDir       string    `json:"data_dir"`
	LockFile      string    `json:"lock_file"`
	Metadata      *Metadata `json:"metadata"`
	MetadataError string    `json:"metadata_error,omitempty"`
}

// FormatStatus renders st to w. When asJSON is true, a single indented
// JSON object is written followed by a trailing newline. Otherwise a
// human-readable multi-line summary is written using the same key
// alignment as the collision banner.
func FormatStatus(w io.Writer, st Status, asJSON bool) error {
	if asJSON {
		payload := statusJSON{
			Running:       st.Running,
			DataDir:       st.DataDir,
			LockFile:      st.LockPath,
			Metadata:      st.Metadata,
			MetadataError: string(st.MetadataUnavailable),
		}
		data, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			return fmt.Errorf("encode status json: %w", err)
		}
		if _, err := w.Write(data); err != nil {
			return err
		}
		_, err = fmt.Fprintln(w)
		return err
	}

	switch {
	case !st.Running:
		fmt.Fprintln(w, "no running daemon")
	case st.Metadata != nil:
		fmt.Fprintln(w, "running")
	default:
		fmt.Fprintf(w, "running (metadata unavailable: %s)\n", st.MetadataUnavailable)
	}

	fmt.Fprintf(w, "  data_dir:     %s\n", st.DataDir)
	fmt.Fprintf(w, "  lock file:    %s\n", st.LockPath)

	if st.Metadata != nil {
		m := st.Metadata
		fmt.Fprintf(w, "  pid:          %d\n", m.PID)
		fmt.Fprintf(w, "  host:         %s\n", m.Host)
		fmt.Fprintf(w, "  port:         %d\n", m.Port)
		fmt.Fprintf(w, "  listen_addr:  %s\n", m.ListenAddr)
		fmt.Fprintf(w, "  started_at:   %s\n", m.StartedAt)
		fmt.Fprintf(w, "  version:      %s\n", m.Version)
		fmt.Fprintf(w, "  commit:       %s\n", m.Commit)
	}

	return nil
}
