package workspace

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"strings"
)

const MaxPastedImageBytes = 10 * 1024 * 1024

var (
	ErrPastedImageTooLarge     = errors.New("pasted image exceeds the size limit")
	ErrUnsupportedPastedImage  = errors.New("unsupported pasted image")
	ErrPastedImagePathConflict = errors.New("pasted image storage path conflicts with an existing file")
)

// StorePastedImage validates decoded clipboard bytes and materializes them
// inside a ready workspace. It returns only a slash-separated workspace path.
func (m *Manager) StorePastedImage(
	ctx context.Context,
	workspaceID string,
	data []byte,
) (string, error) {
	if len(data) > MaxPastedImageBytes {
		return "", ErrPastedImageTooLarge
	}
	extension, ok := pastedImageExtension(http.DetectContentType(data))
	if !ok {
		return "", ErrUnsupportedPastedImage
	}

	ws, err := m.Get(ctx, workspaceID)
	if err != nil {
		return "", err
	}
	if ws == nil {
		return "", ErrWorkspaceNotFound
	}
	if ws.Status != "ready" || strings.TrimSpace(ws.WorktreePath) == "" {
		return "", ErrWorkspaceInvalidState
	}
	if err := preparePastedImageDirectory(ws.WorktreePath); err != nil {
		return "", err
	}
	if err := EnsureWorkspaceGeneratedPathsIgnored(
		ctx, ws.WorktreePath, []string{PastedImageDirectory},
	); err != nil {
		return "", fmt.Errorf("ignore pasted image directory: %w", err)
	}
	return writePastedImageAtomic(ws.WorktreePath, data, extension)
}

func preparePastedImageDirectory(worktreePath string) error {
	root, err := os.OpenRoot(worktreePath)
	if err != nil {
		return fmt.Errorf("open workspace root: %w", err)
	}
	defer root.Close()
	return ensurePastedImageDirectory(root)
}

func pastedImageExtension(contentType string) (string, bool) {
	switch contentType {
	case "image/png":
		return "png", true
	case "image/jpeg":
		return "jpg", true
	case "image/gif":
		return "gif", true
	case "image/webp":
		return "webp", true
	default:
		return "", false
	}
}

func writePastedImageAtomic(worktreePath string, data []byte, extension string) (string, error) {
	root, err := os.OpenRoot(worktreePath)
	if err != nil {
		return "", fmt.Errorf("open workspace root: %w", err)
	}
	defer root.Close()

	if err := ensurePastedImageDirectory(root); err != nil {
		return "", err
	}
	id, err := pastedImageID()
	if err != nil {
		return "", err
	}
	tempPath := path.Join(PastedImageDirectory, ".tmp-paste-"+id)
	finalPath := path.Join(PastedImageDirectory, "paste-"+id+"."+extension)
	installed := false
	defer func() {
		if !installed {
			_ = root.Remove(tempPath)
		}
	}()

	file, err := root.OpenFile(tempPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", fmt.Errorf("create pasted image temporary file: %w", err)
	}
	if err := writeAndSyncPastedImage(file, data); err != nil {
		return "", err
	}
	if err := root.Rename(tempPath, finalPath); err != nil {
		return "", fmt.Errorf("install pasted image: %w", err)
	}
	installed = true
	return finalPath, nil
}

func ensurePastedImageDirectory(root *os.Root) error {
	for _, directory := range []string{".kenn-forge", PastedImageDirectory} {
		info, err := root.Lstat(directory)
		if errors.Is(err, os.ErrNotExist) {
			if err := root.Mkdir(directory, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
				return fmt.Errorf("create pasted image directory %s: %w", directory, err)
			}
			info, err = root.Lstat(directory)
		}
		if err != nil {
			return fmt.Errorf("inspect pasted image directory %s: %w", directory, err)
		}
		if info == nil {
			return fmt.Errorf("inspect pasted image directory %s: missing file information", directory)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("%w: %s", ErrPastedImagePathConflict, directory)
		}
	}
	return nil
}

func writeAndSyncPastedImage(file *os.File, data []byte) error {
	written, writeErr := file.Write(data)
	if writeErr == nil && written != len(data) {
		writeErr = io.ErrShortWrite
	}
	if writeErr != nil {
		_ = file.Close()
		return fmt.Errorf("write pasted image: %w", writeErr)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync pasted image: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close pasted image: %w", err)
	}
	return nil
}

func pastedImageID() (string, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generate pasted image name: %w", err)
	}
	return hex.EncodeToString(random), nil
}
