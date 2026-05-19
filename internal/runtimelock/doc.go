// Package runtimelock guards the middleman daemon against double-launch
// against the same data_dir. Acquire takes an OS-level file lock under
// data_dir before the HTTP listener binds; WriteMetadata records PID,
// listen address, and version under the held lock; Release removes the
// metadata file and unlocks. Read reports liveness via a try-and-release
// probe of the same lock.
package runtimelock
