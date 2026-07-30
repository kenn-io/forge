package docs

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	gitignore "github.com/sabhiram/go-gitignore"
)

// baselineIgnore is the hardcoded floor - these patterns apply even
// when the folder has no .gitignore or .kenn-forgeignore. Kenn Forge is a
// human-notes browser, so VCS metadata, dependency caches, and OS
// clutter are never things the user wants to scroll past, and we
// don't want the user to have to opt every folder into hiding them.
var baselineIgnore = []string{
	".git/",
	".hg/",
	".svn/",
	"node_modules/",
	".DS_Store",
}

const (
	gitIgnoreFilename    = ".gitignore"
	forgeIgnoreFilename  = ".kenn-forgeignore"
	legacyIgnoreFilename = "." + "middle" + "manignore"
)

// ignoreLayer is one ignore source's compiled patterns, anchored at
// the directory the source lived in (relative to the folder root).
// dir is forward-slashed; "" means root.
type ignoreLayer struct {
	dir string
	ig  *gitignore.GitIgnore
}

// folderIgnore answers "is this path hidden from the docs view?"
// for one registered folder. It composes baseline + .kenn-forgeignore
// (root only) + every .gitignore found in the tree. A positive match
// from any layer hides the path - cross-layer negation (parent
// ignores `*.log`, child un-ignores `important.log`) is not
// supported in this version; single-file negation (within one
// .gitignore) still works via the library's own pattern ordering.
type folderIgnore struct {
	layers []ignoreLayer
}

// loadFolderIgnore builds the matcher for one folder root. It walks
// the tree once to discover nested .gitignore files, while honoring
// the layers it has built so far so it doesn't descend into already-
// ignored directories (no point compiling node_modules/**/.gitignore
// just to throw the rules away). Missing/unreadable .gitignore files
// are skipped silently; only top-level walk errors propagate.
func loadFolderIgnore(root string) (*folderIgnore, error) {
	if err := migrateLegacyIgnore(root); err != nil {
		return nil, err
	}
	fi := &folderIgnore{
		layers: []ignoreLayer{
			{dir: "", ig: gitignore.CompileIgnoreLines(baselineIgnore...)},
		},
	}
	if extra, err := compileIgnoreIfExists(filepath.Join(root, forgeIgnoreFilename)); err != nil {
		return nil, err
	} else if extra != nil {
		fi.layers = append(fi.layers, ignoreLayer{dir: "", ig: extra})
	}
	if extra, err := compileIgnoreIfExists(filepath.Join(root, gitIgnoreFilename)); err != nil {
		return nil, err
	} else if extra != nil {
		fi.layers = append(fi.layers, ignoreLayer{dir: "", ig: extra})
	}

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Skip unreadable subtrees rather than aborting the whole
			// load - a single permission-denied directory shouldn't
			// hide every doc in the folder.
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel := relPath(root, path)
		if d.IsDir() {
			if rel != "" && fi.Match(rel, true) {
				return filepath.SkipDir
			}
			return nil
		}
		// Root .gitignore was already pre-loaded above; skip it
		// here so we don't double-apply.
		if rel == gitIgnoreFilename || d.Name() != gitIgnoreFilename {
			return nil
		}
		dir := filepath.ToSlash(filepath.Dir(rel))
		if dir == "." {
			dir = ""
		}
		ig, compileErr := gitignore.CompileIgnoreFile(path)
		if compileErr != nil {
			return nil
		}
		fi.layers = append(fi.layers, ignoreLayer{dir: dir, ig: ig})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return fi, nil
}

func migrateLegacyIgnore(root string) error {
	legacyPath := filepath.Join(root, legacyIgnoreFilename)
	canonicalPath := filepath.Join(root, forgeIgnoreFilename)
	if _, err := os.Stat(legacyPath); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if _, err := os.Stat(canonicalPath); err == nil {
		return fmt.Errorf("both legacy ignore file %s and Kenn Forge ignore file %s exist", legacyPath, canonicalPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(legacyPath, canonicalPath)
}

// Match reports whether relPath (forward-slashed, root-relative)
// should be hidden. isDir flags whether the entry is a directory -
// some gitignore patterns are directory-only ("build/"), and
// passing a trailing slash lets the matcher honor that.
func (fi *folderIgnore) Match(relPath string, isDir bool) bool {
	relPath = strings.TrimPrefix(filepath.ToSlash(relPath), "./")
	if relPath == "" || relPath == "." {
		return false
	}
	for _, layer := range fi.layers {
		var sub string
		switch {
		case layer.dir == "":
			sub = relPath
		case relPath == layer.dir:
			continue
		case !strings.HasPrefix(relPath, layer.dir+"/"):
			continue
		default:
			sub = strings.TrimPrefix(relPath, layer.dir+"/")
		}
		// sabhiram's directory-only patterns ("foo/") only match a
		// path written with the trailing slash, so try both forms
		// when the entry is a directory.
		if layer.ig.MatchesPath(sub) {
			return true
		}
		if isDir && layer.ig.MatchesPath(sub+"/") {
			return true
		}
	}
	return false
}

func compileIgnoreIfExists(path string) (*gitignore.GitIgnore, error) {
	ig, err := gitignore.CompileIgnoreFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return ig, nil
}
