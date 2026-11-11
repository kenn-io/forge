package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/kit/embedconfig"
	"go.kenn.io/kit/embedlocal"
)

func TestLocalEmbeddingsSetup(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	path := filepath.Join(t.TempDir(), "config.toml")
	calls := 0
	deps := localEmbeddingDeps{endpoint: func(_ context.Context, opts embedlocal.Options) (string, error) {
		if calls == 0 {
			assert.False(t, opts.SkipOllama)
		}
		calls++
		return "http://127.0.0.1:1234/v1", nil
	}}
	cmd := newEmbeddingsCommand(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, deps)
	cmd.SetArgs([]string{"local", "--config", path})
	require.NoError(t, cmd.ExecuteContext(t.Context()))
	cfg, err := config.Load(path)
	require.NoError(t, err)
	recorded, err := embedlocal.ChoiceRecorded()
	require.NoError(t, err)
	assert.True(t, recorded)
	assert.Equal(t, "auto", cfg.LocalEmbeddings)
	assert.Equal(t, embedconfig.Embedder{Model: "nomic-embed-text", Dims: 768, DocumentPrefix: "search_document: ", QueryPrefix: "search_query: "}, cfg.Embeddings)
}

func TestLocalEmbeddingsSwitchPreservesSelectionDuringConcurrentEdit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, compatible := range []bool{true, false} {
		t.Run(fmt.Sprint(compatible), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			require.NoError(t, config.EnsureDefault(path))
			initial, err := config.Load(path)
			require.NoError(t, err)
			initial.Embeddings = embedconfig.Embedder{BaseURL: "http://127.0.0.1:1234/v1", Model: " nomic-embed-text:latest ", Dims: 512, RequestDimensions: true, FingerprintSalt: "  ", DocumentPrefix: "custom: "}
			if !compatible {
				initial.Embeddings.Model = "other"
			}
			require.NoError(t, initial.Save(path))
			deps := localEmbeddingDeps{endpoint: func(_ context.Context, opts embedlocal.Options) (string, error) {
				assert.True(t, opts.SkipOllama)
				current, err := config.Load(path)
				require.NoError(t, err)
				current.Port = 9999
				if compatible {
					current.Embeddings.Model = "other"
				} else {
					current.Embeddings.Model = "nomic-embed-text"
				}
				require.NoError(t, current.Save(path))
				return "http://127.0.0.1:1235/v1", nil
			}}
			var output bytes.Buffer
			cmd := newEmbeddingsCommand(strings.NewReader(""), &bytes.Buffer{}, &output, deps)
			cmd.SetArgs([]string{"local", "--config", path, "--yes"})
			require.NoError(t, cmd.ExecuteContext(t.Context()))
			cfg, err := config.Load(path)
			require.NoError(t, err)
			assert.Equal(t, 9999, cfg.Port)
			assert.Equal(t, "builtin", cfg.LocalEmbeddings)
			assert.Empty(t, cfg.Embeddings.BaseURL)
			if compatible {
				assert.Contains(t, output.String(), "keeps the semantic index")
				expected := initial.Embeddings
				expected.BaseURL = ""
				assert.Equal(t, expected, cfg.Embeddings)
			} else {
				assert.Contains(t, output.String(), "rebuild")
				assert.Equal(t, "search_document: ", cfg.Embeddings.DocumentPrefix)
			}
		})
	}
}

func TestLocalEmbeddingsDownloadFailureAndNoninteractiveRefusal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, yes := range []bool{false, true} {
		t.Run(fmt.Sprint(yes), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			original := "[embeddings]\nbase_url = \"http://127.0.0.1:1234/v1\"\nmodel = \"other\"\ndims = 768\n"
			require.NoError(t, os.WriteFile(path, []byte(original), 0o600))
			var output bytes.Buffer
			deps := localEmbeddingDeps{endpoint: func(context.Context, embedlocal.Options) (string, error) { return "", embedlocal.ErrNotFound }, install: func(ctx context.Context, opts embedlocal.InstallOptions) (embedlocal.Installation, error) {
				_, err := opts.Confirm(ctx, 12_345_000)
				if err != nil {
					return embedlocal.Installation{}, err
				}
				return embedlocal.Installation{}, errors.New("download failed")
			}}
			cmd := newEmbeddingsCommand(strings.NewReader(""), &bytes.Buffer{}, &output, deps)
			args := []string{"local", "--config", path}
			if yes {
				args = append(args, "--yes")
			}
			cmd.SetArgs(args)
			err := cmd.ExecuteContext(t.Context())
			if yes {
				require.ErrorContains(t, err, "download failed")
			} else {
				require.ErrorContains(t, err, "non-interactive setup requires --yes")
			}
			assert.Contains(t, output.String(), "12 MB")
			assert.Contains(t, output.String(), "rebuild")
			saved, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, original, string(saved))
			recorded, err := embedlocal.ChoiceRecorded()
			require.NoError(t, err)
			assert.False(t, recorded)
		})
	}
}

func TestLocalEmbeddingsRerun(t *testing.T) {
	for _, mode := range []string{"auto", "builtin"} {
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			path := filepath.Join(t.TempDir(), "config.toml")
			require.NoError(t, config.EnsureDefault(path))
			cfg, err := config.Load(path)
			require.NoError(t, err)
			cfg.LocalEmbeddings = mode
			cfg.Embeddings = embedconfig.Embedder{Model: "nomic-embed-text", Dims: 768, DocumentPrefix: "custom: "}
			require.NoError(t, cfg.Save(path))
			calls := 0
			deps := localEmbeddingDeps{endpoint: func(_ context.Context, opts embedlocal.Options) (string, error) {
				assert.True(t, opts.SkipOllama)
				calls++
				if calls > 1 {
					return "http://127.0.0.1:1235/v1", nil
				}
				return "http://127.0.0.1:1234/v1", nil
			}}
			var output bytes.Buffer
			cmd := newEmbeddingsCommand(strings.NewReader(""), &bytes.Buffer{}, &output, deps)
			args := []string{"local", "--config", path}
			if mode == "auto" {
				args = append(args, "--yes")
			}
			cmd.SetArgs(args)
			require.NoError(t, cmd.ExecuteContext(t.Context()))
			cfg, err = config.Load(path)
			require.NoError(t, err)
			assert.Equal(t, "builtin", cfg.LocalEmbeddings)
			assert.Equal(t, "custom: ", cfg.Embeddings.DocumentPrefix)
			assert.Equal(t, 1, calls)
			recorded, err := embedlocal.ChoiceRecorded()
			require.NoError(t, err)
			assert.True(t, recorded)
			if mode == "auto" {
				assert.Contains(t, output.String(), "keeps the semantic index")
			}
		})
	}
}

func TestLocalEmbeddingsCachedResolution(t *testing.T) {
	calls := 0
	deps := localEmbeddingDeps{
		endpoint: func(_ context.Context, opts embedlocal.Options) (string, error) {
			assert.True(t, opts.SkipOllama)
			calls++
			if calls == 1 {
				return "", embedlocal.ErrNotFound
			}
			assert.Equal(t, "/cache/kennembed", opts.Executable)
			return "http://127.0.0.1:1234/v1", nil
		},
		install: func(_ context.Context, opts embedlocal.InstallOptions) (embedlocal.Installation, error) {
			return embedlocal.Installation{DataDir: "/cache", Executable: "/cache/kennembed"}, nil
		},
	}
	endpoint, err := deps.resolve(t.Context(), true, nil)
	require.NoError(t, err)
	assert.Equal(t, "http://127.0.0.1:1234/v1", endpoint)
	assert.Equal(t, 2, calls)
}

func TestLocalEmbeddingsRecordsBuiltinChoice(t *testing.T) {
	t.Run("Ollama", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("USERPROFILE", home)
		path := filepath.Join(t.TempDir(), "config.toml")
		deps := localEmbeddingDeps{endpoint: func(_ context.Context, opts embedlocal.Options) (string, error) {
			if opts.SkipOllama {
				return "http://127.0.0.1:1235/v1", nil
			}
			return "http://127.0.0.1:1234/v1", nil
		}}
		cmd := newEmbeddingsCommand(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, deps)
		cmd.SetArgs([]string{"local", "--config", path})
		require.NoError(t, cmd.ExecuteContext(t.Context()))
		recorded, err := embedlocal.ChoiceRecorded()
		require.NoError(t, err)
		assert.False(t, recorded)
	})
	t.Run("write failure", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("USERPROFILE", home)
		require.NoError(t, os.WriteFile(filepath.Join(home, ".kenn"), []byte("block"), 0o600))
		path := filepath.Join(t.TempDir(), "config.toml")
		deps := localEmbeddingDeps{endpoint: func(context.Context, embedlocal.Options) (string, error) { return "http://127.0.0.1:1234/v1", nil }}
		var stderr bytes.Buffer
		cmd := newEmbeddingsCommand(strings.NewReader(""), &bytes.Buffer{}, &stderr, deps)
		cmd.SetArgs([]string{"local", "--config", path})
		require.NoError(t, cmd.ExecuteContext(t.Context()))
		assert.Contains(t, stderr.String(), "search by meaning choice write reported an error")
		cfg, err := config.Load(path)
		require.NoError(t, err)
		assert.Equal(t, "auto", cfg.LocalEmbeddings)
	})
}
