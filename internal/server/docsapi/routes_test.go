package docsapi

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/config"
)

func TestPublishLockSetReleasesAndScopesPerFolder(t *testing.T) {
	assert := assert.New(t)
	locks := NewPublishLockSet()

	assert.True(locks.TryAcquire("a"))
	assert.False(locks.TryAcquire("a"))
	assert.True(locks.TryAcquire("b"))
	locks.Release("a")
	assert.True(locks.TryAcquire("a"))
	locks.Release("a")
	locks.Release("b")
}

func TestCreateFolderRollsBackRegistryWhenConfigSaveFails(t *testing.T) {
	initial := config.DocFolder{ID: "existing", Name: "Existing", Path: t.TempDir()}
	h := New(Deps{
		Config: &config.Config{DocFolders: []config.DocFolder{initial}},
		SaveFolders: func([]config.DocFolder) error {
			return errors.New("disk full")
		},
	})
	in := &createDocsFolderInput{}
	in.Body.ID = "new"
	in.Body.Name = "New"
	in.Body.Path = t.TempDir()

	_, err := h.createDocsFolder(context.Background(), in)

	require.Error(t, err)
	folders := h.Folders()
	require.Len(t, folders, 1)
	assert.Equal(t, initial.ID, folders[0].ID)
}
