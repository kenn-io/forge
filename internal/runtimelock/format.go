package runtimelock

import (
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
