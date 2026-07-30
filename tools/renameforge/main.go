package main

import (
	"bytes"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"go.kenn.io/middleman/internal/procutil"
	_ "modernc.org/sqlite"
)

func main() {
	checkOnly := flag.Bool("check", false, "report unapplied Kenn Forge rename changes")
	writeSchemaMigration := flag.Bool("write-schema-migration", false, "write migration 44 from the version-43 schema")
	flag.Parse()

	root, err := os.Getwd()
	if err != nil {
		fatal(err)
	}
	if *writeSchemaMigration {
		if err := writeMigration44(root); err != nil {
			fatal(err)
		}
		return
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
	objects, err := schemaAtVersion43(root)
	if err != nil {
		return err
	}
	up, down, err := RenderSchemaRename(objects)
	if err != nil {
		return err
	}
	dir := filepath.Join(root, "internal", "db", "migrations")
	files := []struct {
		name string
		body []byte
	}{
		{name: "000044_rename_schema_to_forge.up.sql", body: up},
		{name: "000044_rename_schema_to_forge.down.sql", body: down},
	}
	for _, file := range files {
		path := filepath.Join(dir, file.name)
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("migration already exists: %s", path)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect migration %s: %w", path, err)
		}
		if err := os.WriteFile(path, file.body, 0o644); err != nil {
			return fmt.Errorf("write migration %s: %w", path, err)
		}
	}
	return nil
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
		WHERE type IN ('table', 'index', 'trigger')
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
