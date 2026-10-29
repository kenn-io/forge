package localruntime

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.kenn.io/forge/internal/procutil"
)

// acpHostFingerprint records what a shared host's agent ran, so binding a new
// chat can tell from file metadata alone whether the agent changed since. It
// never starts the agent or asks it for a version: that start is the cost a
// shared host exists to avoid. A false alarm costs one cold start.
type acpHostFingerprint struct {
	// Resolved is where the configured command resolved when the host started.
	Resolved string
	// Files are the executables and scripts that ran: the command, a wrapper
	// script, and each process the agent started, with their arguments that
	// name files.
	Files []acpFileStamp
}

type acpFileStamp struct {
	Path    string
	Real    string
	Inode   uint64
	Size    int64
	ModTime time.Time
}

func stampACPFile(path string) (acpFileStamp, bool) {
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return acpFileStamp{}, false
	}
	info, err := os.Stat(realPath)
	if err != nil || !info.Mode().IsRegular() {
		return acpFileStamp{}, false
	}
	return acpFileStamp{Path: path, Real: realPath, Inode: fileInode(info), Size: info.Size(), ModTime: info.ModTime().UTC()}, true
}

// recordACPHostFingerprint stamps the configured command and, where /proc
// exists, every process the agent runs at pid and the files their arguments
// name, relative to dir.
func recordACPHostFingerprint(command []string, dir string, pid int) acpHostFingerprint {
	var fingerprint acpHostFingerprint
	seen := make(map[string]bool)
	add := func(path string) {
		if path == "" || seen[path] {
			return
		}
		seen[path] = true
		if stamp, ok := stampACPFile(path); ok {
			fingerprint.Files = append(fingerprint.Files, stamp)
		}
	}
	if resolved, err := resolveExecutable(command[0]); err == nil {
		fingerprint.Resolved = resolved
		add(resolved)
	}
	var walk func(int)
	walk = func(pid int) {
		proc := filepath.Join("/proc", strconv.Itoa(pid))
		if exe, err := os.Readlink(filepath.Join(proc, "exe")); err == nil {
			add(strings.TrimSuffix(exe, " (deleted)"))
		}
		if cmdline, err := os.ReadFile(filepath.Join(proc, "cmdline")); err == nil {
			for arg := range strings.SplitSeq(strings.TrimRight(string(cmdline), "\x00"), "\x00") {
				if arg == "" || strings.HasPrefix(arg, "-") {
					continue
				}
				if !filepath.IsAbs(arg) {
					arg = filepath.Join(dir, arg)
				}
				add(arg)
			}
		}
		children, _ := filepath.Glob(filepath.Join(proc, "task", "*", "children"))
		for _, file := range children {
			data, _ := os.ReadFile(file)
			for field := range strings.FieldsSeq(string(data)) {
				if child, err := strconv.Atoi(field); err == nil {
					walk(child)
				}
			}
		}
	}
	walk(pid)
	return fingerprint
}

// acpHostStale reports why a host's agent no longer matches what a new start
// would run, or "" when nothing changed: the command resolves elsewhere, a
// file it ran was replaced or changed, or mise now selects another version of
// a tool it ran from.
func acpHostStale(ctx context.Context, fingerprint acpHostFingerprint, command []string, dir string, env []string) string {
	if resolved, err := resolveExecutable(command[0]); err != nil || resolved != fingerprint.Resolved {
		return "the command resolves elsewhere"
	}
	for _, stamp := range fingerprint.Files {
		current, ok := stampACPFile(stamp.Path)
		if !ok || current.Real != stamp.Real || current.Inode != stamp.Inode || current.Size != stamp.Size || !current.ModTime.Equal(stamp.ModTime) {
			return stamp.Path + " changed"
		}
	}
	return miseSelectionChanged(ctx, fingerprint, dir, env)
}

// miseSelectionChanged reports a file the agent ran from a mise install that
// mise no longer selects, as after an upgrade that leaves the old install in
// place. A mise shim's path never changes, and wrapper scripts resolve tools
// through mise themselves, so file stamps alone miss it. mise answers offline
// from its own config in milliseconds.
func miseSelectionChanged(ctx context.Context, fingerprint acpHostFingerprint, dir string, env []string) string {
	if !slices.ContainsFunc(fingerprint.Files, func(stamp acpFileStamp) bool {
		return strings.Contains(stamp.Real, string(filepath.Separator)+"installs"+string(filepath.Separator))
	}) {
		return ""
	}
	mise, err := exec.LookPath("mise")
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := procutil.CommandContext(ctx, mise, "ls", "--current", "--json")
	cmd.Dir = dir
	cmd.Env = append(slices.Clone(env), "MISE_OFFLINE=1")
	output, err := procutil.Output(ctx, cmd, "list current mise tools")
	if err != nil {
		return fmt.Sprintf("mise could not list current tools: %v", err)
	}
	var tools map[string][]struct {
		InstallPath string `json:"install_path"`
	}
	if err := json.Unmarshal(output, &tools); err != nil {
		return fmt.Sprintf("mise listed current tools unreadably: %v", err)
	}
	sep := string(filepath.Separator)
	current := make(map[string][]string) // tool install directory: selected installs
	for _, versions := range tools {
		for _, version := range versions {
			// Only mise's own installs/<tool>/<version> layout; a path: tool
			// can name any directory.
			if version.InstallPath != "" && filepath.Base(filepath.Dir(filepath.Dir(version.InstallPath))) == "installs" {
				toolDir := filepath.Dir(version.InstallPath) + sep
				current[toolDir] = append(current[toolDir], version.InstallPath+sep)
			}
		}
	}
	for toolDir, installs := range current {
		for _, stamp := range fingerprint.Files {
			if strings.HasPrefix(stamp.Real, toolDir) && !slices.ContainsFunc(installs, func(install string) bool { return strings.HasPrefix(stamp.Real, install) }) {
				return "mise no longer selects " + stamp.Real
			}
		}
	}
	return ""
}
