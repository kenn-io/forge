package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/docs"
)

func runDocsCLI(args []string, stdout io.Writer) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		printDocsCLIUsage(stdout)
		if len(args) == 0 {
			return fmt.Errorf("missing docs subcommand")
		}
		return nil
	}
	switch args[0] {
	case "list-folders":
		return runDocsListFolders(args[1:], stdout)
	case "add-folder":
		return runDocsAddFolder(args[1:], stdout)
	case "remove-folder":
		return runDocsRemoveFolder(args[1:], stdout)
	default:
		printDocsCLIUsage(stdout)
		return fmt.Errorf("unknown docs subcommand %q", args[0])
	}
}

func printDocsCLIUsage(w io.Writer) {
	_, _ = fmt.Fprintln(w, "Usage: middleman docs <subcommand> [flags]")
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "Subcommands:")
	_, _ = fmt.Fprintln(w, "  list-folders            List configured docs folders")
	_, _ = fmt.Fprintln(w, "  add-folder <path>       Register a docs folder rooted at path")
	_, _ = fmt.Fprintln(w, "  remove-folder <id>      Drop a docs folder from the config")
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "Global flags:")
	_, _ = fmt.Fprintln(w, "  -config <path>      Override config file")
}

func runDocsListFolders(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("middleman docs list-folders", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configPath := fs.String("config", config.DefaultConfigPath(), "path to config file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("docs list-folders takes no positional args")
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && *configPath == config.DefaultConfigPath() {
			_, _ = fmt.Fprintln(out, "(no config file found)")
			_, _ = fmt.Fprintln(out, "(no folders configured)")
			return nil
		}
		return fmt.Errorf("load config: %w", err)
	}
	_, _ = fmt.Fprintf(out, "config: %s\n", *configPath)
	if len(cfg.DocFolders) == 0 {
		_, _ = fmt.Fprintln(out, "(no folders configured)")
		return nil
	}
	for _, folder := range cfg.DocFolders {
		bind := ""
		if folder.Daemon != "" {
			bind = " -> " + folder.Daemon
		}
		_, _ = fmt.Fprintf(out, "  %s\t%s%s\t%s\n", folder.ID, folder.Name, bind, folder.Path)
	}
	return nil
}

func runDocsAddFolder(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("middleman docs add-folder", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configPath := fs.String("config", config.DefaultConfigPath(), "path to config file")
	id := fs.String("id", "", "folder id")
	name := fs.String("name", "", "display name")
	daemon := fs.String("daemon", "", "bind to a specific Kata daemon id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: middleman docs add-folder [flags] <path>")
	}

	rawPath, err := expandDocsHome(fs.Arg(0))
	if err != nil {
		return fmt.Errorf("expand path: %w", err)
	}
	abs, err := filepath.Abs(rawPath)
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return fmt.Errorf("stat %s: %w", abs, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", abs)
	}

	if err := config.EnsureDefault(*configPath); err != nil {
		return fmt.Errorf("ensure config: %w", err)
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	folderID := strings.TrimSpace(*id)
	if folderID == "" {
		folderID = docs.DeriveFolderID(abs, cfg.DocFolders)
	}
	for _, folder := range cfg.DocFolders {
		if folder.ID == folderID {
			return fmt.Errorf("folder id %q already exists; pass --id to choose another", folderID)
		}
	}
	folderName := strings.TrimSpace(*name)
	if folderName == "" {
		folderName = filepath.Base(abs)
	}

	cfg.DocFolders = append(cfg.DocFolders, config.DocFolder{
		ID:     folderID,
		Name:   folderName,
		Path:   abs,
		Daemon: strings.TrimSpace(*daemon),
	})
	if err := cfg.Save(*configPath); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	_, _ = fmt.Fprintf(out, "added folder %q (%s) at %s\n", folderID, folderName, abs)
	_, _ = fmt.Fprintf(out, "config saved to %s\n", *configPath)
	return nil
}

func runDocsRemoveFolder(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("middleman docs remove-folder", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configPath := fs.String("config", config.DefaultConfigPath(), "path to config file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: middleman docs remove-folder [flags] <id>")
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	id := fs.Arg(0)
	idx := -1
	for i, folder := range cfg.DocFolders {
		if folder.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("folder %q not found in %s", id, *configPath)
	}
	removed := cfg.DocFolders[idx]
	cfg.DocFolders = append(cfg.DocFolders[:idx], cfg.DocFolders[idx+1:]...)
	if err := cfg.Save(*configPath); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	_, _ = fmt.Fprintf(out, "removed folder %q (%s)\n", removed.ID, removed.Path)
	_, _ = fmt.Fprintf(out, "config saved to %s\n", *configPath)
	return nil
}

func expandDocsHome(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if path == "~" {
			return home, nil
		}
		return filepath.Join(home, path[2:]), nil
	}
	return path, nil
}
