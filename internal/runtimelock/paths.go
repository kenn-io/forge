package runtimelock

import "path/filepath"

// File names at the root of data_dir.
const (
	lockFileName     = "forge.lock"
	metadataFileName = "forge.run.json"
	metadataTmpFile  = ".forge.run.json.tmp"
	authTokenLock    = ".auth_token.lock"
)

// LockPath returns the absolute path of the lock file under dataDir.
// The file is created on first Acquire and persists across restarts;
// existence implies nothing about liveness.
func LockPath(dataDir string) string {
	return filepath.Join(dataDir, lockFileName)
}

func authTokenLockPath(dataDir string) string {
	return filepath.Join(dataDir, authTokenLock)
}

// MetadataPath returns the absolute path of the runtime metadata file
// under dataDir. The file exists only while a daemon is running.
func MetadataPath(dataDir string) string {
	return filepath.Join(dataDir, metadataFileName)
}

func metadataTmpPath(dataDir string) string {
	return filepath.Join(dataDir, metadataTmpFile)
}
