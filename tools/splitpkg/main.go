// Command splitpkg splits one large Go package into subpackages following a
// declaration-to-package plan from tools/pkggraph/partition.py.
//
// The source package keeps a hub type (for example a server struct), its
// constructor, and everything the plan leaves in the root. Methods of the
// hub type that move become methods of a per-package Handlers type. Each
// hub field such a method reads becomes a Handlers field: reference-typed
// fields that are only set in the hub's composite literal are copied, and
// every other field is shared through a pointer to the hub's own field so
// runtime reassignment and locking behave exactly as before. Calls to hub
// methods that live in another package become func-typed Handlers fields.
// The root wires every Handlers value right after it allocates the hub.
//
// Identifiers used across the new package boundaries are exported, and
// references everywhere in the module (including other packages that used
// the moved API) are requalified. In-package tests move to the package whose
// code they exercise when that package can see everything they touch;
// shared test helpers and TestMain are copied into each package that needs
// them. The tool refuses to write anything if the resulting package import
// graph has a cycle.
//
// Usage:
//
//	go run ./tools/splitpkg -config tools/splitpkg/server.json [-apply]
package main

import (
	"encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"os"
)

func main() {
	cfgPath := flag.String("config", "", "path to the split config")
	apply := flag.Bool("apply", false, "write files instead of only reporting the plan")
	moves := flag.String("moves", "", "write old-to-new locations of every declaration as JSON")
	flag.Parse()
	if err := run(*cfgPath, *apply, *moves); err != nil {
		fmt.Fprintln(os.Stderr, "splitpkg:", err)
		os.Exit(1)
	}
}

type config struct {
	// Source is the package pattern to split, for example ./internal/server.
	Source string `json:"source"`
	// Plan is the partition.py output mapping unit ids to package labels.
	Plan string `json:"plan"`
	// Names maps plan labels to new package names (directories under Source).
	Names map[string]string `json:"names"`
	// Hub is the god-object type whose methods are redistributed.
	Hub string `json:"hub"`
}

type planFile struct {
	Packages map[string]string `json:"packages"`
}

func run(cfgPath string, apply bool, moves string) error {
	if cfgPath == "" {
		return errors.New("-config is required")
	}
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return err
	}
	var cfg config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}
	rawPlan, err := os.ReadFile(cfg.Plan)
	if err != nil {
		return err
	}
	var pl planFile
	if err := json.Unmarshal(rawPlan, &pl); err != nil {
		return fmt.Errorf("parse plan: %w", err)
	}
	s, err := load(cfg, pl)
	if err != nil {
		return err
	}
	if err := s.place(); err != nil {
		return err
	}
	s.report()
	if err := s.checkImports(); err != nil {
		return err
	}
	if moves != "" {
		if err := s.writeMoves(moves); err != nil {
			return err
		}
	}
	if !apply {
		return nil
	}
	return s.write()
}
