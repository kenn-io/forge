// Package gitfixture builds synthetic Git histories for tests.
package gitfixture

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	gitcmd "go.kenn.io/kit/git/cmd"
)

// AppendFileCommits adds count commits that successively replace path on ref.
// It uses one fast-import process so boundary-size histories do not exhaust
// shared subprocess capacity when Go packages run concurrently.
func AppendFileCommits(t *testing.T, dir, ref, path string, count int) {
	t.Helper()
	require := require.New(t)
	runner := gitcmd.New().WithConfig("gc.auto", "0").WithConfig("maintenance.auto", "false")
	base, err := runner.Output(t.Context(), dir, "rev-parse", ref)
	require.NoError(err)

	var stream bytes.Buffer
	parent := strings.TrimSpace(string(base))
	mark := 1
	startedAt := time.Now().UTC().Unix()
	for i := range count {
		content := fmt.Appendf(nil, "%d\n", i)
		fmt.Fprintf(&stream, "blob\nmark :%d\ndata %d\n", mark, len(content))
		stream.Write(content)
		stream.WriteByte('\n')
		blobMark := mark
		mark++

		message := fmt.Sprintf("churn %03d", i)
		fmt.Fprintf(
			&stream,
			"commit refs/heads/%s\nmark :%d\nauthor Alice <alice@example.com> %d +0000\ncommitter Alice <alice@example.com> %d +0000\ndata %d\n%s\nfrom %s\nM 100644 :%d %s\n\n",
			ref,
			mark,
			startedAt+int64(i),
			startedAt+int64(i),
			len(message),
			message,
			parent,
			blobMark,
			path,
		)
		parent = fmt.Sprintf(":%d", mark)
		mark++
	}

	out, stderr, err := runner.Run(t.Context(), dir, &stream, "fast-import", "--quiet")
	require.NoError(err, "git fast-import failed: %s%s", out, stderr)
	out, stderr, err = runner.Run(t.Context(), dir, nil, "reset", "--hard", ref)
	require.NoError(err, "git reset failed: %s%s", out, stderr)
}
