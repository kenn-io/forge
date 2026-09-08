package landedwork

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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

func TestObjectViewIgnoresGlobalAndSystemConfig(t *testing.T) {
	r := gittest.NewRepoWithCommit(t)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	// Config discovery is the subject: all candidate files are test-owned.
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "xdg"))
	system := filepath.Join(root, "system.gitconfig")
	require := require.New(t)
	require.NoError(os.WriteFile(filepath.Join(root, ".gitconfig"), []byte("[landingprobe]\n global = visible\n"), 0600))
	require.NoError(os.WriteFile(system, []byte("[landingprobe]\n system = visible\n"), 0600))
	m := &meter{limits: Limits{Records: 1000, Nodes: 1000, InputBytes: 1 << 20, OutputBytes: 1 << 20}}
	v, err := openView(ctx, r.Root, m)
	require.NoError(err)
	defer func() { require.NoError(v.close()) }()
	v.runner.Env = append(v.runner.Env, "GIT_CONFIG_SYSTEM="+system)
	// Prove that Git can read both files when the existing protections are off.
	control := v.runner
	control.NullGlobalConfig, control.NoSystemConfig = false, false
	out, err := control.Command(ctx, v.dir, "config", "--get-regexp", "^landingprobe\\.").Output()
	require.NoError(err)
	require.ElementsMatch([]string{"landingprobe.system visible", "landingprobe.global visible"}, strings.Split(strings.TrimSpace(string(out)), "\n"))
	out, err = v.run(ctx, "config", "--get-regexp", "^landingprobe\\.")
	require.Error(err)
	require.Empty(out)
	// The object view remains usable, rather than merely failing every command.
	parents, err := v.parents(ctx, r.Head())
	require.NoError(err)
	require.Empty(parents)
}
