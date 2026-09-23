package runtimetest

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	shellquote "github.com/kballard/go-shellquote"
	"github.com/stretchr/testify/require"
)

func writeRuntimeTmuxLifecycleRecorder(
	t *testing.T,
	dir string,
	record string,
) string {
	t.Helper()
	tmuxPath := filepath.Join(dir, "fake-tmux")
	require.NoError(t, os.WriteFile(tmuxPath, fmt.Appendf(nil, `#!/bin/sh
printf '%%s\0' "$#" "$@" >> %s
target=""
prev=""
for a in "$@"; do
  if [ "$prev" = "-t" ]; then target="$a"; fi
  prev="$a"
done
if [ "$1" = "-u" ]; then shift; fi
case "$1" in
  has-session)
    echo "can't find session: $target" >&2
    exit 1
    ;;
  attach-session)
    cat >/dev/null
    exit 0
    ;;
  new-session|set-option|show-option|kill-session)
    exit 0
    ;;
esac
exit 0
`, shellquote.Join(record)), 0o755))
	return tmuxPath
}
