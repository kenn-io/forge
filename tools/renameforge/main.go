package main

import (
	"bytes"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"go.kenn.io/forge/internal/procutil"
	_ "modernc.org/sqlite"
)

func main() {
	checkOnly := flag.Bool("check", false, "report unapplied Kenn Forge rename changes")
	writeSchemaMigration := flag.Bool("write-schema-migration", false, "write migration 44 from the version-43 schema")
	flag.Parse()
	if err := validateModes(*checkOnly, *writeSchemaMigration); err != nil {
		fatal(err)
	}

	root, err := os.Getwd()
	if err != nil {
		fatal(err)
	}
	if *writeSchemaMigration {
		if err := rewriteMigration44(root); err != nil {
			fatal(err)
		}
		return
	}
	if *checkOnly {
		if err := checkMigration44(root); err != nil {
			fatal(err)
		}
	} else if err := ensureMigration44(root); err != nil {
		fatal(err)
	}
	paths, err := gitTrackedPaths(root)
	if err != nil {
		fatal(err)
	}
	report, err := Rewrite(root, paths, *checkOnly)
	fmt.Printf("changed=%d moved=%d binary=%d allowlisted=%d\n", report.Changed, report.Moved, report.SkippedBinary, report.Allowlisted)
	if err != nil {
		fatal(err)
	}
}

func validateModes(checkOnly, writeSchemaMigration bool) error {
	if checkOnly && writeSchemaMigration {
		return errors.New("--check and --write-schema-migration cannot be combined")
	}
	return nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

func gitTrackedPaths(root string) ([]string, error) {
	cmd := procutil.Command("git", "ls-files", "-z")
	cmd.Dir = root
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("list tracked files: %w", err)
	}
	parts := bytes.Split(output, []byte{0})
	paths := make([]string, 0, len(parts))
	for _, part := range parts {
		if len(part) > 0 {
			paths = append(paths, string(part))
		}
	}
	return paths, nil
}

func writeMigration44(root string) error {
	up, down, err := renderMigration44(root)
	if err != nil {
		return err
	}
	return writeMigrationPair(filepath.Join(root, "internal", "db", "migrations"), up, down)
}

func rewriteMigration44(root string) error {
	up, down, err := renderMigration44(root)
	if err != nil {
		return err
	}
	dir := filepath.Join(root, "internal", "db", "migrations")
	for _, file := range []struct {
		name string
		body []byte
	}{
		{name: upMigration44, body: up},
		{name: downMigration44, body: down},
	} {
		temp, err := writeMigrationTemp(dir, file.name, file.body)
		if err != nil {
			return err
		}
		if err := os.Rename(temp, filepath.Join(dir, file.name)); err != nil {
			_ = os.Remove(temp)
			return fmt.Errorf("replace migration %s: %w", file.name, err)
		}
	}
	return nil
}

func renderMigration44(root string) ([]byte, []byte, error) {
	objects, err := schemaAtVersion43(root)
	if err != nil {
		return nil, nil, err
	}
	up, down, err := RenderSchemaRename(objects)
	if err != nil {
		return nil, nil, err
	}
	return up, down, nil
}

const (
	upMigration44   = "000044_rename_schema_to_forge.up.sql"
	downMigration44 = "000044_rename_schema_to_forge.down.sql"
)

func ensureMigration44(root string) error {
	dir := filepath.Join(root, "internal", "db", "migrations")
	upPath := filepath.Join(dir, upMigration44)
	downPath := filepath.Join(dir, downMigration44)
	_, upErr := os.Stat(upPath)
	_, downErr := os.Stat(downPath)
	if os.IsNotExist(upErr) && os.IsNotExist(downErr) {
		return writeMigration44(root)
	}
	return checkMigration44(root)
}

func checkMigration44(root string) error {
	up, down, err := renderMigration44(root)
	if err != nil {
		return err
	}
	return verifyMigrationPair(filepath.Join(root, "internal", "db", "migrations"), up, down)
}

func verifyMigrationPair(dir string, wantUp, wantDown []byte) error {
	for _, file := range []struct {
		name string
		want []byte
	}{
		{name: upMigration44, want: wantUp},
		{name: downMigration44, want: wantDown},
	} {
		got, err := os.ReadFile(filepath.Join(dir, file.name))
		if err != nil {
			return fmt.Errorf("read migration %s: %w", file.name, err)
		}
		if !bytes.Equal(got, file.want) {
			return fmt.Errorf("migration %s differs from generated output", file.name)
		}
	}
	return nil
}

func writeMigrationPair(dir string, up, down []byte) error {
	for _, name := range []string{upMigration44, downMigration44} {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("migration already exists: %s", path)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect migration %s: %w", path, err)
		}
	}

	upTemp, err := writeMigrationTemp(dir, upMigration44, up)
	if err != nil {
		return err
	}
	defer os.Remove(upTemp)
	downTemp, err := writeMigrationTemp(dir, downMigration44, down)
	if err != nil {
		return err
	}
	defer os.Remove(downTemp)

	upPath := filepath.Join(dir, upMigration44)
	if err := os.Rename(upTemp, upPath); err != nil {
		return fmt.Errorf("publish migration %s: %w", upMigration44, err)
	}
	downPath := filepath.Join(dir, downMigration44)
	if err := os.Rename(downTemp, downPath); err != nil {
		if removeErr := os.Remove(upPath); removeErr != nil {
			return fmt.Errorf("publish migration %s: %w (rollback %s: %v)", downMigration44, err, upMigration44, removeErr)
		}
		return fmt.Errorf("publish migration %s: %w", downMigration44, err)
	}
	return nil
}

func writeMigrationTemp(dir, name string, body []byte) (string, error) {
	file, err := os.CreateTemp(dir, "."+name+"-*")
	if err != nil {
		return "", fmt.Errorf("create migration temp for %s: %w", name, err)
	}
	path := file.Name()
	if _, err := file.Write(body); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("write migration temp for %s: %w", name, err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("sync migration temp for %s: %w", name, err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("close migration temp for %s: %w", name, err)
	}
	return path, nil
}

func schemaAtVersion43(root string) ([]SchemaObject, error) {
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, fmt.Errorf("open schema snapshot database: %w", err)
	}
	defer database.Close()

	pattern := filepath.Join(root, "internal", "db", "migrations", "*.up.sql")
	paths, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("list migrations: %w", err)
	}
	sort.Strings(paths)
	for _, path := range paths {
		version, err := strconv.Atoi(filepath.Base(path)[:6])
		if err != nil {
			return nil, fmt.Errorf("parse migration version %s: %w", path, err)
		}
		if version > 43 {
			continue
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", path, err)
		}
		if _, err := database.Exec(string(body)); err != nil {
			return nil, fmt.Errorf("apply migration %s: %w", path, err)
		}
	}

	rows, err := database.Query(`
		SELECT type, name, tbl_name, sql
		FROM sqlite_schema
		WHERE type IN ('table', 'index', 'trigger', 'view')
		  AND name NOT LIKE 'sqlite_%'
		  AND sql IS NOT NULL
		ORDER BY type, name`)
	if err != nil {
		return nil, fmt.Errorf("query schema snapshot: %w", err)
	}
	defer rows.Close()

	var objects []SchemaObject
	for rows.Next() {
		var object SchemaObject
		if err := rows.Scan(&object.Type, &object.Name, &object.Table, &object.SQL); err != nil {
			return nil, fmt.Errorf("scan schema object: %w", err)
		}
		objects = append(objects, object)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read schema objects: %w", err)
	}
	return objects, nil
}
