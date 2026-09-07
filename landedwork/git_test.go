package landedwork

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gittest "go.kenn.io/kit/git/test"
)

func TestObjectCommandReapsCanceledProcess(t *testing.T) {
	r := gittest.NewRepoWithCommit(t)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	m := &meter{limits: Limits{Records: 1000, Nodes: 1000, InputBytes: 1 << 20, OutputBytes: 1 << 20}}
	v, err := openView(ctx, r.Root, m)
	require := require.New(t)
	require.NoError(err)
	defer func() { require.NoError(v.close()) }()
	input, writer, err := os.Pipe()
	require.NoError(err)
	defer input.Close()
	defer writer.Close()
	cmd := v.command(ctx, "cat-file", "--batch")
	cmd.Stdin = input
	out, err := cmd.StdoutPipe()
	require.NoError(err)
	require.NoError(cmd.Start())
	defer func() {
		cancel()
		if cmd.ProcessState == nil {
			_ = cmd.Wait()
		}
	}()
	_, err = fmt.Fprintln(writer, r.Head())
	require.NoError(err)
	reader := bufio.NewReader(out)
	header, err := reader.ReadString('\n')
	require.NoError(err)
	var id, kind string
	var size int64
	_, err = fmt.Sscanf(header, "%s %s %d", &id, &kind, &size)
	require.NoError(err)
	require.Equal(r.Head(), id)
	require.Equal("commit", kind)
	_, err = io.CopyN(io.Discard, reader, size+1)
	require.NoError(err)
	// The input stays open: cancellation, not EOF, must stop and reap Git.
	cancel()
	require.Error(cmd.Wait())
	require.NotNil(cmd.ProcessState)
	assert.False(t, cmd.ProcessState.Success())
	require.ErrorIs(ctx.Err(), context.Canceled)
}
