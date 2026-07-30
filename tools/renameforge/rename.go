package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

var (
	ErrChangesRequired         = errors.New("rename requires changes")
	ErrPathCollision           = errors.New("rename path collision")
	ErrUnsupportedSchemaObject = errors.New("unsupported SQLite schema object")
)

type Report struct {
	Changed       int
	Moved         int
	SkippedBinary int
	Allowlisted   int
}

type plannedFile struct {
	OldPath string
	NewPath string
}

func Rewrite(root string, paths []string, checkOnly bool) (Report, error) {
	files := make([]plannedFile, 0, len(paths))
	for _, path := range paths {
		cleaned := filepath.ToSlash(filepath.Clean(path))
		newPath := cleaned
		if !isAllowlistedPath(cleaned) {
			newPath = rewritePath(cleaned)
		}
		files = append(files, plannedFile{OldPath: cleaned, NewPath: newPath})
	}

	if err := rejectCollisions(root, files); err != nil {
		return Report{}, err
	}

	var report Report
	for i := range files {
		if isAllowlistedPath(files[i].OldPath) {
			report.Allowlisted++
			continue
		}
		changed, binary, err := fileNeedsRewrite(root, files[i])
		if err != nil {
			return Report{}, err
		}
		if binary {
			report.SkippedBinary++
			continue
		}
		if changed {
			report.Changed++
		}
		if files[i].OldPath != files[i].NewPath {
			report.Moved++
		}
	}

	if checkOnly {
		if report.Changed > 0 {
			return report, ErrChangesRequired
		}
		return report, nil
	}

	sort.SliceStable(files, func(i, j int) bool {
		return len(files[i].OldPath) > len(files[j].OldPath)
	})
	for _, file := range files {
		if isAllowlistedPath(file.OldPath) {
			continue
		}
		if err := applyFileRewrite(root, file); err != nil {
			return Report{}, err
		}
	}
	return report, nil
}

func rejectCollisions(root string, files []plannedFile) error {
	sources := make(map[string]struct{}, len(files))
	for _, file := range files {
		sources[file.OldPath] = struct{}{}
	}
	for _, file := range files {
		if file.OldPath == file.NewPath {
			continue
		}
		if _, movingAway := sources[file.NewPath]; movingAway {
			return fmt.Errorf("%w: %s -> %s", ErrPathCollision, file.OldPath, file.NewPath)
		}
		_, err := os.Lstat(filepath.Join(root, filepath.FromSlash(file.NewPath)))
		if err == nil {
			return fmt.Errorf("%w: %s -> %s", ErrPathCollision, file.OldPath, file.NewPath)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect rename destination %s: %w", file.NewPath, err)
		}
	}
	return nil
}

func fileNeedsRewrite(root string, file plannedFile) (changed, binary bool, err error) {
	fullPath := filepath.Join(root, filepath.FromSlash(file.OldPath))
	info, err := os.Lstat(fullPath)
	if err != nil {
		return false, false, fmt.Errorf("inspect %s: %w", file.OldPath, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(fullPath)
		if err != nil {
			return false, false, fmt.Errorf("read symlink %s: %w", file.OldPath, err)
		}
		return file.OldPath != file.NewPath || rewritePath(target) != target, false, nil
	}
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return false, false, fmt.Errorf("read %s: %w", file.OldPath, err)
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return false, true, nil
	}
	rewritten := rewriteContent(file.NewPath, string(data))
	return file.OldPath != file.NewPath || rewritten != string(data), false, nil
}

func applyFileRewrite(root string, file plannedFile) error {
	oldFullPath := filepath.Join(root, filepath.FromSlash(file.OldPath))
	info, err := os.Lstat(oldFullPath)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", file.OldPath, err)
	}

	if info.Mode()&os.ModeSymlink != 0 {
		return rewriteSymlink(root, file)
	}
	data, err := os.ReadFile(oldFullPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", file.OldPath, err)
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return nil
	}
	rewritten := []byte(rewriteContent(file.NewPath, string(data)))
	newFullPath := filepath.Join(root, filepath.FromSlash(file.NewPath))
	if file.OldPath != file.NewPath {
		if err := os.MkdirAll(filepath.Dir(newFullPath), 0o755); err != nil {
			return fmt.Errorf("create rename parent for %s: %w", file.NewPath, err)
		}
		if err := os.Rename(oldFullPath, newFullPath); err != nil {
			return fmt.Errorf("rename %s to %s: %w", file.OldPath, file.NewPath, err)
		}
	}
	if !bytes.Equal(data, rewritten) {
		if err := os.WriteFile(newFullPath, rewritten, info.Mode().Perm()); err != nil {
			return fmt.Errorf("rewrite %s: %w", file.NewPath, err)
		}
	}
	return nil
}

func rewriteSymlink(root string, file plannedFile) error {
	oldFullPath := filepath.Join(root, filepath.FromSlash(file.OldPath))
	target, err := os.Readlink(oldFullPath)
	if err != nil {
		return fmt.Errorf("read symlink %s: %w", file.OldPath, err)
	}
	newTarget := rewritePath(target)
	newFullPath := filepath.Join(root, filepath.FromSlash(file.NewPath))
	if file.OldPath == file.NewPath && target == newTarget {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(newFullPath), 0o755); err != nil {
		return fmt.Errorf("create symlink parent for %s: %w", file.NewPath, err)
	}
	if err := os.Remove(oldFullPath); err != nil {
		return fmt.Errorf("remove old symlink %s: %w", file.OldPath, err)
	}
	if err := os.Symlink(newTarget, newFullPath); err != nil {
		return fmt.Errorf("write symlink %s: %w", file.NewPath, err)
	}
	return nil
}

type SchemaObject struct {
	Type  string
	Name  string
	Table string
	SQL   string
}

func RenderSchemaRename(objects []SchemaObject) ([]byte, []byte, error) {
	objects = slices.Clone(objects)
	sort.Slice(objects, func(i, j int) bool {
		if objects[i].Type == objects[j].Type {
			return objects[i].Name < objects[j].Name
		}
		return objects[i].Type < objects[j].Type
	})
	for _, object := range objects {
		switch object.Type {
		case "index", "table", "trigger":
		default:
			return nil, nil, fmt.Errorf("%w: %s %s", ErrUnsupportedSchemaObject, object.Type, object.Name)
		}
	}

	var up, down strings.Builder
	writeDrops(&up, objects, "middleman")
	writeTableRenames(&up, objects, "middleman", "forge", false)
	writeCreates(&up, objects, "middleman", "forge")

	writeDrops(&down, objects, "forge")
	writeTableRenames(&down, objects, "middleman", "forge", true)
	writeCreates(&down, objects, "middleman", "middleman")
	return []byte(up.String()), []byte(down.String()), nil
}

func writeDrops(builder *strings.Builder, objects []SchemaObject, existing string) {
	for _, object := range objects {
		if object.Type != "index" && object.Type != "trigger" {
			continue
		}
		name := object.Name
		if existing == "forge" {
			name = strings.ReplaceAll(name, "middleman", "forge")
		}
		if strings.Contains(name, existing) {
			fmt.Fprintf(builder, "DROP %s %s;\n", strings.ToUpper(object.Type), name)
		}
	}
}

func writeTableRenames(builder *strings.Builder, objects []SchemaObject, old, new string, reverse bool) {
	if reverse {
		slices.Reverse(objects)
	}
	for _, object := range objects {
		if object.Type != "table" || !strings.Contains(object.Name, old) {
			continue
		}
		from, to := object.Name, strings.ReplaceAll(object.Name, old, new)
		if reverse {
			from, to = to, from
		}
		fmt.Fprintf(builder, "ALTER TABLE %s RENAME TO %s;\n", from, to)
	}
}

func writeCreates(builder *strings.Builder, objects []SchemaObject, old, new string) {
	for _, object := range objects {
		if (object.Type == "index" || object.Type == "trigger") && strings.Contains(object.Name, old) {
			fmt.Fprintln(builder, strings.TrimSuffix(strings.ReplaceAll(object.SQL, old, new), ";")+";")
		}
	}
}
