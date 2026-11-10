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
	for _, ollama := range []bool{false, true} {
		t.Run(fmt.Sprint(ollama), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			deps := localEmbeddingDeps{
				inspect: func(context.Context, embedlocal.Options) (embedlocal.Status, error) {
					return embedlocal.Status{OllamaAvailable: ollama}, nil
				},
				endpoint: func(_ context.Context, opts embedlocal.Options) (string, error) {
					assert.True(t, opts.SkipOllama)
					return "http://127.0.0.1:1234/v1", nil
				},
			}
			cmd := newEmbeddingsCommand(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, deps)
			cmd.SetArgs([]string{"local", "--config", path})
			require.NoError(t, cmd.ExecuteContext(t.Context()))
			cfg, err := config.Load(path)
			require.NoError(t, err)
			assert.Equal(t, !ollama, cfg.LocalEmbeddings)
			if ollama {
				assert.Equal(t, "http://127.0.0.1:11434/v1", cfg.Embeddings.BaseURL)
			} else {
				assert.Empty(t, cfg.Embeddings.BaseURL)
			}
			assert.Equal(t, "nomic-embed-text", cfg.Embeddings.Model)
			assert.Equal(t, 768, cfg.Embeddings.Dims)
			assert.Equal(t, "search_document: ", cfg.Embeddings.DocumentPrefix)
			assert.Equal(t, "search_query: ", cfg.Embeddings.QueryPrefix)
		})
	}
}

func TestLocalEmbeddingsSwitchPreservesSelectionDuringConcurrentEdit(t *testing.T) {
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
			assert.True(t, cfg.LocalEmbeddings)
			assert.Empty(t, cfg.Embeddings.BaseURL)
			if compatible {
				assert.Contains(t, output.String(), "keeps the semantic index")
				assert.Equal(t, " nomic-embed-text:latest ", cfg.Embeddings.Model)
				assert.Equal(t, "  ", cfg.Embeddings.FingerprintSalt)
				assert.Equal(t, 512, cfg.Embeddings.Dims)
				assert.True(t, cfg.Embeddings.RequestDimensions)
				assert.Equal(t, "custom: ", cfg.Embeddings.DocumentPrefix)
			} else {
				assert.Contains(t, output.String(), "rebuild")
				assert.Equal(t, "search_document: ", cfg.Embeddings.DocumentPrefix)
			}
		})
	}
}

func TestLocalEmbeddingsDownloadFailureAndNoninteractiveRefusal(t *testing.T) {
	for _, yes := range []bool{false, true} {
		t.Run(fmt.Sprint(yes), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			original := "[embeddings]\nbase_url = \"http://127.0.0.1:1234/v1\"\nmodel = \"other\"\ndims = 768\n"
			require.NoError(t, os.WriteFile(path, []byte(original), 0o600))
			var output bytes.Buffer
			deps := localEmbeddingDeps{endpoint: func(context.Context, embedlocal.Options) (string, error) { return "", embedlocal.ErrNotFound }, dir: func() (string, error) { return t.TempDir(), nil }, install: func(ctx context.Context, opts embedlocal.InstallOptions) (embedlocal.Installation, error) {
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
		})
	}
}

func TestLocalEmbeddingsBuiltinRerun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, config.EnsureDefault(path))
	cfg, err := config.Load(path)
	require.NoError(t, err)
	cfg.LocalEmbeddings = true
	cfg.Embeddings = embedconfig.Embedder{Model: "nomic-embed-text", Dims: 768, DocumentPrefix: "custom: "}
	require.NoError(t, cfg.Save(path))
	deps := localEmbeddingDeps{endpoint: func(_ context.Context, opts embedlocal.Options) (string, error) {
		assert.True(t, opts.SkipOllama)
		return "http://127.0.0.1:1234/v1", nil
	}}
	cmd := newEmbeddingsCommand(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, deps)
	cmd.SetArgs([]string{"local", "--config", path})
	require.NoError(t, cmd.ExecuteContext(t.Context()))
	cfg, err = config.Load(path)
	require.NoError(t, err)
	assert.True(t, cfg.LocalEmbeddings)
	assert.Equal(t, "custom: ", cfg.Embeddings.DocumentPrefix)
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
		dir: func() (string, error) { return "/cache", nil },
		install: func(_ context.Context, opts embedlocal.InstallOptions) (embedlocal.Installation, error) {
			return embedlocal.Installation{DataDir: "/cache", Executable: "/cache/kennembed"}, nil
		},
	}
	endpoint, err := deps.resolve(t.Context(), true, nil)
	require.NoError(t, err)
	assert.Equal(t, "http://127.0.0.1:1234/v1", endpoint)
	assert.Equal(t, 2, calls)
}
