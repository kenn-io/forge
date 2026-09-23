package accesstest

import (
	"io/fs"
	"testing/fstest"
)

func emptyFrontend() fs.FS {
	return fstest.MapFS{
		"index.html": &fstest.MapFile{
			Data: []byte("<!DOCTYPE html><html><body>ok</body></html>"),
		},
	}
}
