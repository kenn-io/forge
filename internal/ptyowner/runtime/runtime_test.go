package runtime

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHasRelativePathSyntaxRejectsWindowsPathForms(t *testing.T) {
	assert := assert.New(t)

	assert.False(hasRelativePathSyntax("tool.exe"))
	assert.True(hasRelativePathSyntax(`dir\\tool.exe`))
	assert.True(hasRelativePathSyntax("./tool.exe"))
	assert.True(hasRelativePathSyntax("C:tool.exe"))
}

func TestResolveExecutableRejectsPathSyntaxEvenIfPATHContainsMatch(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	cases := []string{"./tool.exe", "C:tool.exe"}
	if runtime.GOOS != "windows" {
		name := `dir\tool`
		exe := filepath.Join(dir, name)
		require.NoError(os.WriteFile(exe, []byte("#!/bin/sh\nexit 0\n"), 0o755))
		cases = append(cases, name)
	}

	for _, name := range cases {
		_, err := ResolveExecutable(name)
		require.Error(err)
		assert.Contains(err.Error(), "relative paths resolve inside")
	}
}
