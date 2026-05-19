package runtimelock

import "path/filepath"

// File names at the root of data_dir.
const (
	lockFileName     = "middleman.lock"
	metadataFileName = "middleman.run.json"
	metadataTmpFile  = ".middleman.run.json.tmp"
)

// LockPath returns the absolute path of the lock file under dataDir.
// The file is created on first Acquire and persists across restarts;
// existence implies nothing about liveness.
func LockPath(dataDir string) string {
	return filepath.Join(dataDir, lockFileName)
}

// MetadataPath returns the absolute path of the runtime metadata file
// under dataDir. The file exists only while a daemon is running.
func MetadataPath(dataDir string) string {
	return filepath.Join(dataDir, metadataFileName)
}

func metadataTmpPath(dataDir string) string {
	return filepath.Join(dataDir, metadataTmpFile)
}
