package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/docs"
)

func runDocsCLI(args []string, stdout io.Writer) error {
	cmd := newDocsCommand(stdout)
	cmd.SetOut(stdout)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(normalizeSingleDashLongFlags(args))
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	return cmd.Execute()
}

func newDocsCommand(out io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "docs",
		Short: "Manage documentation folders",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(
		newDocsListFoldersCommand(out),
		newDocsAddFolderCommand(out),
		newDocsRemoveFolderCommand(out),
	)
	return cmd
}

func runDocsListFolders(args []string, out io.Writer) error {
	cmd := newDocsListFoldersCommand(out)
	cmd.SetArgs(normalizeSingleDashLongFlags(args))
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	return cmd.Execute()
}

func newDocsListFoldersCommand(out io.Writer) *cobra.Command {
	var configPath string
	cmd := &cobra.Command{
		Use:   "list-folders",
		Short: "List configured documentation folders",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return listDocsFolders(configPath, out)
		},
	}
	cmd.Flags().StringVar(&configPath, "config", config.DefaultConfigPath(), "path to config file")
	return cmd
}

func listDocsFolders(configPath string, out io.Writer) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && configPath == config.DefaultConfigPath() {
			_, _ = fmt.Fprintln(out, "(no config file found)")
			_, _ = fmt.Fprintln(out, "(no folders configured)")
			return nil
		}
		return fmt.Errorf("load config: %w", err)
	}
	_, _ = fmt.Fprintf(out, "config: %s\n", configPath)
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
	cmd := newDocsAddFolderCommand(out)
	cmd.SetArgs(normalizeSingleDashLongFlags(args))
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	return cmd.Execute()
}

func newDocsAddFolderCommand(out io.Writer) *cobra.Command {
	var configPath, id, name, daemon string
	cmd := &cobra.Command{
		Use:   "add-folder PATH",
		Short: "Register a documentation folder",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return addDocsFolder(configPath, id, name, daemon, args[0], out)
		},
	}
	cmd.Flags().StringVar(&configPath, "config", config.DefaultConfigPath(), "path to config file")
	cmd.Flags().StringVar(&id, "id", "", "folder id")
	cmd.Flags().StringVar(&name, "name", "", "display name")
	cmd.Flags().StringVar(&daemon, "daemon", "", "bind to a specific Kata daemon id")
	return cmd
}

func addDocsFolder(configPath, id, name, daemon, path string, out io.Writer) error {
	rawPath, err := expandDocsHome(path)
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

	if err := config.EnsureDefault(configPath); err != nil {
		return fmt.Errorf("ensure config: %w", err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	folderID := strings.TrimSpace(id)
	if folderID == "" {
		folderID = docs.DeriveFolderID(abs, cfg.DocFolders)
	}
	for _, folder := range cfg.DocFolders {
		if folder.ID == folderID {
			return fmt.Errorf("folder id %q already exists; pass --id to choose another", folderID)
		}
	}
	folderName := strings.TrimSpace(name)
	if folderName == "" {
		folderName = filepath.Base(abs)
	}

	cfg.DocFolders = append(cfg.DocFolders, config.DocFolder{
		ID:     folderID,
		Name:   folderName,
		Path:   abs,
		Daemon: strings.TrimSpace(daemon),
	})
	if err := cfg.Save(configPath); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	_, _ = fmt.Fprintf(out, "added folder %q (%s) at %s\n", folderID, folderName, abs)
	_, _ = fmt.Fprintf(out, "config saved to %s\n", configPath)
	return nil
}

func runDocsRemoveFolder(args []string, out io.Writer) error {
	cmd := newDocsRemoveFolderCommand(out)
	cmd.SetArgs(normalizeSingleDashLongFlags(args))
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	return cmd.Execute()
}

func newDocsRemoveFolderCommand(out io.Writer) *cobra.Command {
	var configPath string
	cmd := &cobra.Command{
		Use:   "remove-folder ID",
		Short: "Remove a documentation folder",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return removeDocsFolder(configPath, args[0], out)
		},
	}
	cmd.Flags().StringVar(&configPath, "config", config.DefaultConfigPath(), "path to config file")
	return cmd
}

func removeDocsFolder(configPath, id string, out io.Writer) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	idx := -1
	for i, folder := range cfg.DocFolders {
		if folder.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("folder %q not found in %s", id, configPath)
	}
	removed := cfg.DocFolders[idx]
	cfg.DocFolders = append(cfg.DocFolders[:idx], cfg.DocFolders[idx+1:]...)
	if err := cfg.Save(configPath); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	_, _ = fmt.Fprintf(out, "removed folder %q (%s)\n", removed.ID, removed.Path)
	_, _ = fmt.Fprintf(out, "config saved to %s\n", configPath)
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
