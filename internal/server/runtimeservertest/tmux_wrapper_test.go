package runtimeservertest

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func readTmuxRecord(t *testing.T, path string) [][]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	// Split on NUL. Each record is "<argc>\0<arg0>\0<arg1>\0...\0",
	// so a flushed stream always ends with a trailing \0 and Split
	// produces a final empty element after it. Strip exactly one
	// trailing empty so we don't mistake it for part of the next
	// record. Interior empty elements are real args (the NUL framing
	// exists to preserve them) and must NOT be skipped.
	parts := strings.Split(string(data), "\x00")
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	var out [][]string
	for i := 0; i < len(parts); {
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			// Trailing record is mid-write: argc isn't a valid
			// integer yet. Stop; the next poll will see the full
			// record once the recorder script flushes.
			break
		}
		if i+1+n > len(parts) {
			// argc is parsed but not all args are on disk yet.
			// Same treatment: defer to the next poll.
			break
		}
		i++
		argv := parts[i : i+n]
		for j := range argv {
			argv[j] = normalizeRecordedTmuxArg(argv[j])
		}
		out = append(out, argv)
		i += n
	}
	return out
}

func normalizeRecordedTmuxArg(arg string) string {
	if runtime.GOOS != "windows" {
		return arg
	}
	switch arg {
	case "#session_name":
		return "#{session_name}"
	case "#pane_title":
		return "#{pane_title}"
	default:
		return arg
	}
}
